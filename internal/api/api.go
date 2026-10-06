package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	"github.com/hutuyee/ShitIDC/internal/config"
	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/extension"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/mail"
	"github.com/hutuyee/ShitIDC/internal/model"
	"github.com/hutuyee/ShitIDC/internal/queue"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

type App struct {
	Store   *store.Store
	Redis   *redis.Client
	Queue   *queue.Client
	Cfg     config.Config
	Bus     *events.Bus
	ExtHost *extension.Host
}

type principal struct {
	User        model.User
	Permissions map[string]bool
	CSRF        string
	APIToken    bool
}

const principalKey = "principal"

func NewRouter(a *App) *gin.Engine {
	if a.Cfg.AppEnv == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	if err := r.SetTrustedProxies(a.Cfg.TrustedProxies); err != nil {
		panic("invalid TRUSTED_PROXIES: " + err.Error())
	}
	r.Use(gin.Logger(), gin.Recovery(), requestID(), a.apiLogger(), a.rateLimit())
	r.GET("/healthz", a.health)
	r.GET("/api/v1/version", a.version)
	r.Static("/docs", "./docs")
	r.Static("/themes", "./themes")

	v1 := r.Group("/api/v1")
	{
		v1.GET("/auth/config", a.authConfig)
		v1.GET("/auth/captcha", a.issueCaptcha)
		v1.POST("/auth/register", a.register)
		v1.GET("/auth/register-fields", a.registerClientFields)
		v1.POST("/auth/register/email-code", a.sendEmailCode)
		// 短信验证码（注册 / 登录 / 绑定 / 重置）
		v1.POST("/auth/sms-code", a.sendSMSCode)
		v1.POST("/auth/sms-code/verify", a.verifySMSCode)
		// 第三方登录：列出可用通道 + 发起授权 + 平台回调
		v1.GET("/auth/oauth/providers", a.listOAuthProviders)
		v1.GET("/auth/oauth/:provider/start", a.oauthStart)
		v1.GET("/auth/oauth/:provider/callback", a.oauthCallback)
		// 企业微信「指令回调 URL」：接收 suite_ticket（GET 校验 / POST 推送）
		v1.GET("/auth/qyweixin/receive", a.oauthQyweixinReceive)
		v1.POST("/auth/qyweixin/receive", a.oauthQyweixinReceive)
		v1.POST("/auth/verify-email", a.verifyEmail)
		v1.POST("/auth/login", a.login)
		v1.POST("/auth/password/reset/request", a.requestPasswordReset)
		v1.POST("/auth/password/reset/confirm", a.confirmPasswordReset)
		v1.GET("/products", a.listProducts)
		v1.GET("/products/:id/prices", a.productPrices)
		// 下单页需要的配置项与自定义字段
		v1.GET("/products/:id/config", a.productConfig)
		// 多币种独立定价：该商品在每个币种下的价格
		v1.GET("/products/:id/price-currencies", a.productPriceCurrencies)
		// 客户组差异定价：这个用户实际要付多少（展示用，结算时服务端重算）
		v1.GET("/products/:id/quote", a.quoteProductPrice)
		v1.GET("/currencies", a.listCurrencies)
		v1.GET("/branding", a.branding)
		v1.GET("/product-groups", a.listProductGroups)
		v1.GET("/announcements", a.listAnnouncements)
		v1.GET("/announcements/:id", a.announcementDetail)
		// Public payment gateway callbacks (signature-verified inside).
		// The generic per-method routes come from the PaymentProvider
		// abstraction (第七阶段); the epay aliases stay for existing configs.
		v1.Any("/pay/notify/:method", a.payNotify)
		v1.GET("/pay/return/:method", a.payReturn)
		v1.Any("/pay/epay/notify", func(c *gin.Context) {
			c.Params = append(c.Params, gin.Param{Key: "method", Value: "epay"})
			a.payNotify(c)
		})
		v1.GET("/pay/epay/return", func(c *gin.Context) {
			c.Params = append(c.Params, gin.Param{Key: "method", Value: "epay"})
			a.payReturn(c)
		})

		authed := v1.Group("")
		authed.Use(a.auth())
		{
			authed.POST("/auth/logout", a.csrf(), a.logout)
			authed.POST("/auth/password/change", a.csrf(), a.changePassword)
			authed.POST("/auth/2fa/setup", a.csrf(), a.totpSetup)
			authed.POST("/auth/2fa/enable", a.csrf(), a.totpEnable)
			authed.POST("/auth/2fa/disable", a.csrf(), a.totpDisable)
			authed.GET("/auth/2fa/status", a.totpStatus)
			authed.GET("/auth/me", a.me)
			authed.GET("/sessions", a.listSessions)
			authed.POST("/coupons/validate", a.require("order.read"), a.csrf(), a.validateCoupon)
			// 代金券（对齐魔方 IdcsmartVoucher 插件）
			authed.GET("/vouchers/my", a.require("order.read"), a.myVouchers)
			authed.GET("/vouchers/claimable", a.require("order.read"), a.claimableVouchers)
			authed.POST("/vouchers/preview", a.require("order.read"), a.csrf(), a.previewVoucher)
			authed.POST("/vouchers/:id/claim", a.require("order.read"), a.csrf(), a.claimVoucher)
			// 活动促销（对齐魔方 EventPromotion 插件）：进行中的活动展示
			authed.GET("/promotions/active", a.require("order.read"), a.myRunningPromotions)
			authed.GET("/referral", a.myReferral)
			// 推介计划（对齐魔方 IdcsmartRecommend 插件）：开启计划、推介链接、
			// 建议推介产品、奖励记录、推介政策与提现申请。
			authed.GET("/recommend", a.myRecommendAwards)
			authed.GET("/recommend/promoter", a.myRecommendPromoter)
			authed.POST("/recommend/promoter", a.csrf(), a.openRecommendPromoter)
			authed.GET("/recommend/description", a.myRecommendDescription)
			authed.GET("/recommend/promoter/system_url", a.myRecommendSystemURLs)
			authed.GET("/recommend/promoter/url", a.myRecommendLinks)
			authed.POST("/recommend/promoter/url", a.csrf(), a.createRecommendLink)
			authed.DELETE("/recommend/promoter/url/:id", a.csrf(), a.deleteRecommendLink)
			authed.GET("/recommend/copy_link", a.recommendCopyLink)
			authed.GET("/recommend/products", a.myRecommendProducts)
			authed.GET("/recommend/config", a.myRecommendPolicy)
			authed.GET("/product-dropdown-select", a.myProductDropDown)
			authed.GET("/e-contract/orders", a.require("order.read"), a.myEContractOrders)
			authed.GET("/e-contracts/my", a.require("order.read"), a.myEContracts)
			authed.POST("/e-contracts/apply", a.require("order.read"), a.csrf(), a.myApplyEContract)
			authed.POST("/e-contracts/my/:id/sign", a.require("order.read"), a.csrf(), a.mySignEContract)
			authed.GET("/e-contracts/my/:id/download", a.require("order.read"), a.myDownloadEContract)
			authed.GET("/recommend/withdrawals", a.myRecommendWithdrawals)
			authed.POST("/recommend/withdraw", a.csrf(), a.applyRecommendWithdraw)
			authed.GET("/notifications", a.listNotifications)
			authed.POST("/notifications/:id/read", a.csrf(), a.markNotification)
			authed.POST("/tickets/:id/attachments", a.require("ticket.write"), a.csrf(), a.uploadTicketAttachment)
			authed.GET("/attachments/:id", a.downloadAttachment)
			authed.DELETE("/sessions/:id", a.csrf(), a.revokeSession)
			authed.POST("/sessions/revoke-others", a.csrf(), a.revokeOtherSessions)
			authed.GET("/orders", a.require("order.read"), a.listOrders)
			authed.POST("/orders", a.require("order.read"), a.csrf(), a.createOrder)
			authed.POST("/orders/:id/pay", a.require("wallet.read"), a.csrf(), a.payOrder)
			authed.POST("/orders/:id/pay/online", a.require("wallet.read"), a.csrf(), a.payOrderOnline)
			authed.POST("/orders/:id/cancel", a.require("order.read"), a.csrf(), a.cancelOrder)
			authed.GET("/payment-methods", a.require("wallet.read"), a.listPaymentMethods)
			authed.POST("/wallet/recharge", a.require("wallet.read"), a.csrf(), a.rechargeWallet)
			authed.GET("/invoices", a.require("invoice.read"), a.listInvoices)
			authed.GET("/invoices/:id", a.require("invoice.read"), a.invoiceDetail)
			// 发票申请（对齐魔方 IdcsmartInvoice 插件；后台开启 invoice_manage 后可用）
			authed.GET("/invoice_config", a.require("order.read"), a.myInvoiceConfig)
			authed.GET("/invoice_title", a.require("order.read"), a.listMyInvoiceTitles)
			authed.POST("/invoice_title", a.require("order.read"), a.csrf(), a.createMyInvoiceTitle)
			authed.PUT("/invoice_title/:id", a.require("order.read"), a.csrf(), a.updateMyInvoiceTitle)
			authed.DELETE("/invoice_title/:id", a.require("order.read"), a.csrf(), a.deleteMyInvoiceTitle)
			authed.GET("/invoice_address", a.require("order.read"), a.listMyInvoiceAddresses)
			authed.POST("/invoice_address", a.require("order.read"), a.csrf(), a.createMyInvoiceAddress)
			authed.PUT("/invoice_address/:id", a.require("order.read"), a.csrf(), a.updateMyInvoiceAddress)
			authed.DELETE("/invoice_address/:id", a.require("order.read"), a.csrf(), a.deleteMyInvoiceAddress)
			authed.GET("/invoice_project", a.require("order.read"), a.listMyInvoiceProjects)
			authed.GET("/invoice_request", a.require("order.read"), a.listMyInvoiceRequestableOrders)
			authed.POST("/invoice/price", a.require("order.read"), a.csrf(), a.quoteMyInvoice)
			authed.GET("/invoice", a.require("order.read"), a.listMyInvoiceRequests)
			authed.POST("/invoice", a.require("order.read"), a.csrf(), a.createMyInvoiceRequest)
			authed.GET("/invoice/:id", a.require("order.read"), a.getMyInvoiceRequest)
			authed.DELETE("/invoice/:id", a.require("order.read"), a.csrf(), a.cancelMyInvoiceRequest)
			authed.GET("/invoice/:id/invoice_filename", a.require("order.read"), a.myInvoiceFile)
			authed.GET("/wallet", a.require("wallet.read"), a.wallet)
			authed.GET("/wallet/transactions", a.require("wallet.read"), a.walletTransactions)
			authed.GET("/services", a.require("service.read"), a.listServices)
			authed.GET("/services/:id", a.require("service.read"), a.serviceDetail)
			authed.POST("/services/:id/renew", a.require("order.read"), a.csrf(), a.renewService)
			// 后付费：查看授信 + 下赊账订单
			authed.GET("/credit", a.require("order.read"), a.myCredit)
			authed.POST("/orders/postpaid", a.require("order.create"), a.csrf(), a.createPostpaidOrder)
			// 购物车多商品结算
			authed.GET("/cart", a.require("order.read"), a.getCart)
			authed.POST("/cart/items", a.require("order.create"), a.csrf(), a.addCartItem)
			authed.DELETE("/cart/items/:id", a.require("order.create"), a.csrf(), a.removeCartItem)
			authed.DELETE("/cart", a.require("order.create"), a.csrf(), a.clearCart)
			authed.POST("/cart/checkout", a.require("order.create"), a.csrf(), a.checkoutCart)
			authed.POST("/checkouts/:id/pay", a.require("order.create"), a.csrf(), a.payCheckout)
			// 第三方登录绑定管理
			authed.GET("/oauth/identities", a.require("profile.read"), a.myOAuthIdentities)
			authed.DELETE("/oauth/identities/:provider", a.require("profile.update"), a.csrf(), a.unbindOAuthIdentity)
			// 手机号绑定（短信验证码 purpose=bind 的落地处）
			authed.POST("/profile/phone", a.require("profile.update"), a.csrf(), a.bindPhone)
			authed.DELETE("/profile/phone", a.require("profile.update"), a.csrf(), a.unbindPhone)
			// 实名认证
			authed.GET("/certification", a.require("profile.read"), a.myCertification)
			authed.POST("/certification", a.require("profile.update"), a.csrf(), a.submitCertification)
			authed.GET("/certification/poll", a.require("profile.read"), a.pollCertification)
			// 按量 / 超量计费：查看用量与待出账费用，上报用量
			authed.GET("/services/:id/usage", a.require("service.read"), a.serviceUsage)
			authed.POST("/services/:id/usage", a.require("service.operate"), a.csrf(), a.reportServiceUsage)
			// 升降级（魔方 shd_upgrades）：列出方案、报价、提交
			authed.GET("/services/:id/upgrade-plans", a.require("service.read"), a.listUpgradePlans)
			authed.POST("/services/:id/upgrade/quote", a.require("service.read"), a.csrf(), a.quoteUpgrade)
			authed.POST("/services/:id/upgrade", a.require("service.read"), a.csrf(), a.requestUpgrade)
			// 流量包（对齐魔方 FlowPacket 插件）：为名下关联产品购买，余额支付
			authed.GET("/flow-packets", a.require("service.read"), a.listMyFlowPackets)
			authed.GET("/flow-packets/orders", a.require("service.read"), a.listMyFlowPacketOrders)
			authed.POST("/flow-packets/:id/purchase", a.require("service.read"), a.csrf(), a.purchaseFlowPacket)
			authed.POST("/flow-packets/orders/:id/pay", a.require("service.read"), a.csrf(), a.payFlowPacketOrder)
			authed.POST("/flow-packets/orders/:id/cancel", a.require("service.read"), a.csrf(), a.cancelFlowPacketOrder)
			// 客户关怀站内信（对齐魔方 ClientCare 插件）
			authed.GET("/client-care/mails", a.listMyClientCareMails)
			authed.GET("/client-care/mails/:id", a.getMyClientCareMail)
			authed.POST("/client-care/mails/:id/read", a.csrf(), a.readMyClientCareMail)
			authed.GET("/tickets", a.require("ticket.read"), a.listTickets)
			authed.POST("/tickets", a.require("ticket.write"), a.csrf(), a.createTicket)
			authed.GET("/tickets/:id", a.require("ticket.read"), a.ticketDetail)
			authed.POST("/tickets/:id/reply", a.require("ticket.write"), a.csrf(), a.replyTicket)
			authed.POST("/tickets/:id/close", a.require("ticket.write"), a.csrf(), a.closeTicket)
			// 工单高级版（对齐魔方 TicketPremium 插件）：元数据 / 部门 / 关联产品 / 催单 / 评分
			authed.GET("/tickets/meta", a.require("ticket.read"), a.ticketPremiumMeta)
			authed.GET("/tickets/departments", a.require("ticket.read"), a.ticketPremiumDepartments)
			authed.GET("/tickets/hosts", a.require("ticket.read"), a.ticketPremiumHosts)
			authed.POST("/tickets/:id/urge", a.require("ticket.write"), a.csrf(), a.urgeTicket)
			authed.POST("/tickets/:id/score", a.require("ticket.write"), a.csrf(), a.scoreTicket)
			authed.GET("/profile/custom-fields", a.require("profile.read"), a.myClientFields)
			authed.PUT("/profile/custom-fields", a.require("profile.update"), a.csrf(), a.saveMyClientFields)
			authed.GET("/profile", a.require("profile.manage"), a.getProfile)
			authed.PUT("/profile", a.require("profile.manage"), a.csrf(), a.updateProfile)
			authed.GET("/api-tokens", a.require("api_token.manage"), a.listTokens)
			authed.POST("/api-tokens", a.require("api_token.manage"), a.csrf(), a.createToken)
			authed.DELETE("/api-tokens/:id", a.require("api_token.manage"), a.csrf(), a.revokeToken)

			// 后台 API 挂在可配置前缀上（默认 /admin-panel，见 config.defaultAdminPath）。
			// 路由表只写一次（registerAdminRoutes），挂到主前缀与 /admin 兼容前缀上：
			// 老前端与已经发出去的文档链接不会失效。两个前缀的 handler 与中间件完全相同，
			// 鉴权没有任何差别——权限判断仍在每个路由自己的 require(...) 上。
			// 后台前缀必须在这里再归一化一次：Config 未必经 Load() 构造（测试、内嵌调用），
			// AdminPath 为空会让后台路由挂到根路径，与上面的用户路由冲突并 panic。
			adminPath := config.NormalizeAdminPath(a.Cfg.AdminPath)
			a.registerAdminRoutes(authed.Group(adminPath))
			if adminPath != "/admin" {
				a.registerAdminRoutes(authed.Group("/admin"))
			}
		}
	}

	compat := r.Group("/compat/magiccube/v1")
	// Upstream compatibility login: a downstream 魔方财务 exchanges its
	// ShitIDC API token (keyID.secret) for a jwt-shaped token. Registered
	// before the auth middleware because it validates credentials itself.
	compat.POST("/login_api", a.compatMagicCubeLogin)
	compat.Use(a.auth())
	{
		compat.GET("/products", a.require("product.read"), a.compatProducts)
		// Resource operations for downstream panels treating ShitIDC as
		// upstream (第三十阶段). create/renew debit the token owner's wallet.
		compat.POST("/create", a.require("order.read"), a.compatCreateService)
		compat.POST("/suspend", a.require("service.operate"), a.compatServiceAction("suspend"))
		compat.POST("/unsuspend", a.require("service.operate"), a.compatServiceAction("unsuspend"))
		compat.POST("/terminate", a.require("service.operate"), a.compatServiceAction("terminate"))
		compat.POST("/renew", a.require("service.operate"), a.compatRenewService)
	}

	// 魔方（ZJMF）"服务器模块"协议：签名（time+random+token → md5 大写）自校验，
	// 因此不套用 ShitIDC 的 Bearer 认证。模块用 curl 发 x-www-form-urlencoded，
	// 同一个入口既是测试连接也是商品列表和主机生命周期。
	mc := r.Group("/compat/magiccube/v1")
	mc.Use(a.requireUpstreamKey())
	{
		mc.POST("/test", a.upstreamBusinessTest)
		mc.GET("/test", a.upstreamBusinessTest)
		mc.POST("/product", a.upstreamProductList)
		mc.GET("/product", a.upstreamProductList)
		mc.POST("/host", a.upstreamHostDispatch)
		mc.GET("/host", a.upstreamHostDispatch)
		mc.POST("/host/create", a.upstreamHostCreate)
		mc.POST("/host/status", a.upstreamHostStatus)
		mc.POST("/host/sync", a.upstreamHostSync)
		mc.POST("/host/locked", a.upstreamHostTransitionHandler(actionSuspend))
		mc.POST("/host/start", a.upstreamHostTransitionHandler(actionUnsuspend))
		mc.POST("/host/recycle", a.upstreamHostTransitionHandler(actionTerminate))
		mc.POST("/host/renew", a.upstreamHostRenew)
	}
	return r
}

// 动作名常量，避免在路由表里散落字符串。
const (
	actionSuspend   = "suspend"
	actionUnsuspend = "unsuspend"
	actionTerminate = "terminate"
)

// registerAdminRoutes 把全部后台接口注册到给定的路由组上。
//
// 抽成函数是为了让主前缀与兼容前缀共用**同一份**路由表：
// 将来新增接口只改这一处，不会出现「某个前缀少了几个接口」这种只在特定部署下暴露的偏差。
func (a *App) registerAdminRoutes(g *gin.RouterGroup) {
	g.GET("/users", a.require("user.read"), a.adminListUsers)
	// 统一搜索：后台选用户/商品/服务/订单都用它，不必手抄 UUID。
	g.GET("/search", a.require("user.read"), a.adminSearch)
	// 管理端待办事项（对应魔方 widget/ToDo 插件）：按权限返回待处理工单 / 待审实名 / 开通中服务数量。
	g.GET("/todos", a.adminTodos)
	// 用户详情聚合：一次拿到余额、机器、订单、授信，供「点开用户」抽屉使用。
	g.GET("/users/:id/detail", a.require("user.read"), a.adminUserDetail)
	g.GET("/tickets", a.require("ticket.manage"), a.adminListTickets)
	g.GET("/tickets/:id", a.require("ticket.manage"), a.adminTicketDetail)
	g.POST("/tickets/:id/reply", a.require("ticket.manage"), a.csrf(), a.adminReplyTicket)
	g.POST("/tickets/:id/status", a.require("ticket.manage"), a.csrf(), a.adminTicketStatus)
	g.GET("/products", a.require("product.read"), a.adminListProducts)
	g.POST("/products", a.require("product.write"), a.csrf(), a.adminCreateProduct)
	g.PUT("/products/:id", a.require("product.write"), a.csrf(), a.adminUpdateProduct)
	// 商品配置项（魔方可配置选项）与自定义字段
	// 多币种独立定价：查看/维护一个商品在每个币种下的价格
	g.GET("/products/:id/prices", a.require("product.read"), a.adminListProductPrices)
	g.GET("/products/:id/config-options", a.require("product.read"), a.adminListConfigOptions)
	g.POST("/products/:id/config-options", a.require("product.write"), a.csrf(), a.adminCreateConfigOption)
	g.PUT("/products/:id/config-options/:option_id", a.require("product.write"), a.csrf(), a.adminUpdateConfigOption)
	g.DELETE("/products/:id/config-options/:option_id", a.require("product.write"), a.csrf(), a.adminDeleteConfigOption)
	g.GET("/products/:id/config-links", a.require("product.read"), a.adminListConfigLinks)
	g.POST("/products/:id/config-links", a.require("product.write"), a.csrf(), a.adminCreateConfigLink)
	g.GET("/products/:id/custom-fields", a.require("product.read"), a.adminListCustomFields)
	g.POST("/products/:id/custom-fields", a.require("product.write"), a.csrf(), a.adminCreateCustomField)
	g.DELETE("/products/:id/custom-fields/:field_id", a.require("product.write"), a.csrf(), a.adminDeleteCustomField)
	g.GET("/product-groups", a.require("product.read"), a.listProductGroups)
	g.POST("/product-groups", a.require("product.write"), a.csrf(), a.adminCreateProductGroup)
	g.PUT("/product-groups/:id", a.require("product.write"), a.csrf(), a.adminUpdateProductGroup)
	g.DELETE("/product-groups/:id", a.require("product.write"), a.csrf(), a.adminDeleteProductGroup)
	g.GET("/product-dropdown-select", a.require("product.write"), a.adminGetProductDropDown)
	g.PUT("/product-dropdown-select", a.require("product.write"), a.csrf(), a.adminSaveProductDropDown)
	g.GET("/announcements", a.require("announcement.manage"), a.adminListAnnouncements)
	g.POST("/announcements", a.require("announcement.manage"), a.csrf(), a.adminCreateAnnouncement)
	g.PUT("/announcements/:id", a.require("announcement.manage"), a.csrf(), a.adminUpdateAnnouncement)
	g.DELETE("/announcements/:id", a.require("announcement.manage"), a.csrf(), a.adminDeleteAnnouncement)
	g.GET("/payment-methods/supported", a.require("wallet.adjust"), a.listSupportedPaymentMethods)
	g.POST("/wallet/adjust", a.require("wallet.adjust"), a.csrf(), a.adminAdjustWallet)
	g.GET("/orders", a.require("order.read"), a.adminListOrders)
	g.GET("/payment-providers", a.require("wallet.adjust"), a.adminListPaymentProviders)
	g.POST("/payment-providers", a.require("wallet.adjust"), a.csrf(), a.adminCreatePaymentProvider)
	g.PUT("/payment-providers/:id", a.require("wallet.adjust"), a.csrf(), a.adminUpdatePaymentProvider)
	g.GET("/settings/mail", a.require("provider.manage"), a.adminGetMailSettings)
	g.PUT("/settings/mail", a.require("provider.manage"), a.csrf(), a.adminSaveMailSettings)
	g.POST("/settings/mail/test", a.require("provider.manage"), a.csrf(), a.adminTestMail)
	g.GET("/providers", a.require("provider.manage"), a.adminListProviders)
	g.POST("/providers", a.require("provider.manage"), a.csrf(), a.adminCreateProvider)
	g.PUT("/providers/:id", a.require("provider.manage"), a.csrf(), a.adminUpdateProvider)
	g.POST("/providers/magiccube/test", a.require("provider.manage"), a.csrf(), a.testMagicCube)
	g.POST("/providers/zjmf-import", a.require("provider.manage"), a.csrf(), a.adminZJMFImport)
	g.GET("/providers/:id/spec", a.require("provider.manage"), a.adminGetProviderSpec)
	g.PUT("/providers/:id/spec", a.require("provider.manage"), a.csrf(), a.adminUpdateProviderSpec)
	g.POST("/providers/:id/test", a.require("provider.manage"), a.csrf(), a.adminTestProvider)
	g.POST("/providers/:id/sync", a.require("provider.manage"), a.csrf(), a.adminSyncProvider)
	g.GET("/providers/:id/products", a.require("provider.manage"), a.adminProviderProducts)
	g.POST("/providers/:id/products/:upstream_id/import", a.require("product.write"), a.csrf(), a.adminImportProviderProduct)
	// 魔方（ZJMF）上游访问密钥：把 ShitIDC 作为魔方财务的上游接口。
	// 超量计费：手动出账与账单明细
	g.POST("/services/:id/overage/settle", a.require("service.operate"), a.csrf(), a.adminSettleOverage)
	g.GET("/services/:id/overage/charges", a.require("service.read"), a.adminListOverageCharges)
	// 短信通道与发送流水
	g.GET("/sms-providers", a.require("settings.manage"), a.adminListSmsProviders)
	g.POST("/sms-providers", a.require("settings.manage"), a.csrf(), a.adminCreateSmsProvider)
	g.POST("/sms-providers/:id/default", a.require("settings.manage"), a.csrf(), a.adminSetDefaultSmsProvider)
	g.DELETE("/sms-providers/:id", a.require("settings.manage"), a.csrf(), a.adminDeleteSmsProvider)
	g.GET("/sms-messages", a.require("settings.manage"), a.adminListSmsMessages)
	// 邮件通道（SMTP 之外的 API 邮件服务；没有启用中的通道时回退内置 SMTP）
	g.GET("/mail-providers", a.require("settings.manage"), a.adminListMailProviders)
	g.POST("/mail-providers", a.require("settings.manage"), a.csrf(), a.adminCreateMailProvider)
	g.POST("/mail-providers/:id/default", a.require("settings.manage"), a.csrf(), a.adminSetDefaultMailProvider)
	g.POST("/mail-providers/:id/test", a.require("settings.manage"), a.csrf(), a.adminTestMailProvider)
	g.DELETE("/mail-providers/:id", a.require("settings.manage"), a.csrf(), a.adminDeleteMailProvider)

	// 人机验证通道：内置图形验证码之外的可插拔通道
	g.GET("/captcha-providers", a.require("settings.manage"), a.adminListCaptchaProviders)
	g.POST("/captcha-providers", a.require("settings.manage"), a.csrf(), a.adminCreateCaptchaProvider)
	g.POST("/captcha-providers/:id/default", a.require("settings.manage"), a.csrf(), a.adminSetDefaultCaptchaProvider)
	g.DELETE("/captcha-providers/:id", a.require("settings.manage"), a.csrf(), a.adminDeleteCaptchaProvider)
	// 对象存储通道（工单附件转存；未配置时附件仍存本机）
	g.GET("/oss-providers", a.require("settings.manage"), a.adminListOssProviders)
	g.POST("/oss-providers", a.require("settings.manage"), a.csrf(), a.adminCreateOssProvider)
	g.POST("/oss-providers/:id/default", a.require("settings.manage"), a.csrf(), a.adminSetDefaultOssProvider)
	g.POST("/oss-providers/:id/test", a.require("settings.manage"), a.csrf(), a.adminTestOssProvider)
	g.DELETE("/oss-providers/:id", a.require("settings.manage"), a.csrf(), a.adminDeleteOssProvider)
	// 第三方登录通道
	g.GET("/oauth-providers", a.require("settings.manage"), a.adminListOAuthProviders)
	g.POST("/oauth-providers", a.require("settings.manage"), a.csrf(), a.adminSaveOAuthProvider)
	g.POST("/oauth-providers/:id/toggle", a.require("settings.manage"), a.csrf(), a.adminToggleOAuthProvider)
	g.DELETE("/oauth-providers/:id", a.require("settings.manage"), a.csrf(), a.adminDeleteOAuthProvider)
	// 后付费授信管理
	g.GET("/credit-accounts", a.require("user.manage"), a.adminListCreditAccounts)
	g.POST("/users/:id/credit", a.require("user.manage"), a.csrf(), a.adminSetUserCredit)
	// 实名认证审核
	g.GET("/certifications", a.require("user.manage"), a.adminListCertifications)
	g.POST("/certifications/:id/review", a.require("user.manage"), a.csrf(), a.adminReviewCertification)
	g.POST("/certification/required", a.require("settings.manage"), a.csrf(), a.adminSetCertificationRequired)
	// 实名核验通道管理（manual / alitwo 等）
	g.GET("/certification-providers", a.require("settings.manage"), a.adminListCertificationProviders)
	g.POST("/certification-providers", a.require("settings.manage"), a.csrf(), a.adminCreateCertificationProvider)
	g.POST("/certification-providers/:id/default", a.require("settings.manage"), a.csrf(), a.adminSetDefaultCertificationProvider)
	g.DELETE("/certification-providers/:id", a.require("settings.manage"), a.csrf(), a.adminDeleteCertificationProvider)
	// 客户组按产品差异定价
	g.GET("/user-groups/:id/prices", a.require("user.manage"), a.adminListUserProductPrices)
	g.POST("/user-groups/:id/prices", a.require("user.manage"), a.csrf(), a.adminSetUserProductPrice)
	g.DELETE("/user-groups/:id/prices/:product_id", a.require("user.manage"), a.csrf(), a.adminDeleteUserProductPrice)
	// 接口分组与容量分配
	g.GET("/provider-groups", a.require("provider.manage"), a.adminListProviderGroups)
	g.POST("/provider-groups", a.require("provider.manage"), a.csrf(), a.adminCreateProviderGroup)
	g.DELETE("/provider-groups/:id", a.require("provider.manage"), a.csrf(), a.adminDeleteProviderGroup)
	g.GET("/provider-groups/:id/members", a.require("provider.manage"), a.adminListProviderGroupMembers)
	g.POST("/provider-groups/:id/members", a.require("provider.manage"), a.csrf(), a.adminAddProviderGroupMember)
	g.DELETE("/provider-groups/:id/members/:provider_id", a.require("provider.manage"), a.csrf(), a.adminRemoveProviderGroupMember)
	g.GET("/upstream-keys", a.require("provider.manage"), a.adminListUpstreamKeys)
	g.POST("/upstream-keys", a.require("provider.manage"), a.csrf(), a.adminCreateUpstreamKey)
	g.PUT("/upstream-keys/:id", a.require("provider.manage"), a.csrf(), a.adminUpdateUpstreamKey)
	g.DELETE("/upstream-keys/:id", a.require("provider.manage"), a.csrf(), a.adminDeleteUpstreamKey)
	g.GET("/upstream-keys/:id/links", a.require("provider.manage"), a.adminUpstreamKeyLinks)
	g.GET("/coupons", a.require("coupon.manage"), a.adminListCoupons)
	g.POST("/coupons", a.require("coupon.manage"), a.csrf(), a.adminCreateCoupon)
	g.PUT("/coupons/:id", a.require("coupon.manage"), a.csrf(), a.adminUpdateCoupon)
	g.DELETE("/coupons/:id", a.require("coupon.manage"), a.csrf(), a.adminDeleteCoupon)
	// 代金券（对齐魔方 IdcsmartVoucher 插件；后台发放 / 前台领取，下单时核销）
	g.GET("/vouchers", a.require("voucher.manage"), a.adminListVouchers)
	g.GET("/vouchers/check", a.require("voucher.manage"), a.adminCheckVoucherCode)
	g.GET("/vouchers/record", a.require("voucher.manage"), a.adminVoucherRecords)
	g.POST("/vouchers", a.require("voucher.manage"), a.csrf(), a.adminCreateVoucher)
	g.GET("/vouchers/:id", a.require("voucher.manage"), a.adminGetVoucher)
	g.PUT("/vouchers/:id", a.require("voucher.manage"), a.csrf(), a.adminUpdateVoucher)
	g.DELETE("/vouchers/:id", a.require("voucher.manage"), a.csrf(), a.adminDeleteVoucher)
	g.POST("/vouchers/:id/enable", a.require("voucher.manage"), a.csrf(), a.adminEnableVoucher)
	g.POST("/vouchers/:id/disable", a.require("voucher.manage"), a.csrf(), a.adminDisableVoucher)
	g.POST("/vouchers/:id/times", a.require("voucher.manage"), a.csrf(), a.adminVoucherTimes)
	g.POST("/vouchers/:id/send", a.require("voucher.manage"), a.csrf(), a.adminSendVoucher)
	g.DELETE("/vouchers/record/:id", a.require("voucher.manage"), a.csrf(), a.adminDeleteVoucherRecord)
	// 活动促销（对齐魔方 EventPromotion 插件；下单时自动生效，无需填码）
	g.GET("/promotions", a.require("promotion.manage"), a.adminListPromotions)
	g.GET("/promotions/active", a.require("promotion.manage"), a.adminActivePromotions)
	g.POST("/promotions", a.require("promotion.manage"), a.csrf(), a.adminCreatePromotion)
	g.PUT("/promotions/order", a.require("promotion.manage"), a.csrf(), a.adminReorderPromotions)
	g.PUT("/promotions/config", a.require("promotion.manage"), a.csrf(), a.adminSavePromotionConfig)
	g.GET("/promotions/:id", a.require("promotion.manage"), a.adminGetPromotion)
	g.PUT("/promotions/:id", a.require("promotion.manage"), a.csrf(), a.adminUpdatePromotion)
	g.DELETE("/promotions/:id", a.require("promotion.manage"), a.csrf(), a.adminDeletePromotion)
	g.PUT("/promotions/:id/status", a.require("promotion.manage"), a.csrf(), a.adminSetPromotionStatus)
	// 周期人工订单（对齐魔方 CycleArtificialOrder 插件；调度器定时为用户生成人工订单）
	g.GET("/cycle-artificial-orders", a.require("cycle_order.manage"), a.adminListCycleArtificialOrders)
	g.POST("/cycle-artificial-orders", a.require("cycle_order.manage"), a.csrf(), a.adminCreateCycleArtificialOrder)
	g.POST("/cycle-artificial-orders/batch-delete", a.require("cycle_order.manage"), a.csrf(), a.adminBatchDeleteArtificialOrders)
	g.GET("/cycle-artificial-orders/:id", a.require("cycle_order.manage"), a.adminGetCycleArtificialOrder)
	g.PUT("/cycle-artificial-orders/:id", a.require("cycle_order.manage"), a.csrf(), a.adminUpdateCycleArtificialOrder)
	g.DELETE("/cycle-artificial-orders/:id", a.require("cycle_order.manage"), a.csrf(), a.adminDeleteCycleArtificialOrder)
	g.PUT("/artificial-orders/:id/price", a.require("cycle_order.manage"), a.csrf(), a.adminAdjustArtificialOrder)
	g.POST("/artificial-orders/:id/mark-paid", a.require("cycle_order.manage"), a.csrf(), a.adminMarkArtificialOrderPaid)
	g.DELETE("/artificial-orders/:id", a.require("cycle_order.manage"), a.csrf(), a.adminDeleteArtificialOrder)
	// 发票申请（对齐魔方 IdcsmartInvoice 插件）
	g.GET("/invoice", a.require("invoice.manage"), a.adminListInvoiceRequests)
	g.GET("/invoice_config", a.require("invoice.manage"), a.adminGetInvoiceConfig)
	g.PUT("/invoice_config", a.require("invoice.manage"), a.csrf(), a.adminSaveInvoiceConfig)
	g.GET("/invoice_project", a.require("invoice.manage"), a.adminListInvoiceProjects)
	g.POST("/invoice_project", a.require("invoice.manage"), a.csrf(), a.adminCreateInvoiceProject)
	g.PUT("/invoice_project/:id", a.require("invoice.manage"), a.csrf(), a.adminUpdateInvoiceProject)
	g.DELETE("/invoice_project/:id", a.require("invoice.manage"), a.csrf(), a.adminDeleteInvoiceProject)
	g.GET("/invoice_title", a.require("invoice.manage"), a.adminListInvoiceTitles)
	g.DELETE("/invoice_title", a.require("invoice.manage"), a.csrf(), a.adminBatchDeleteInvoiceTitles)
	g.GET("/invoice_address", a.require("invoice.manage"), a.adminListInvoiceAddresses)
	g.DELETE("/invoice_address", a.require("invoice.manage"), a.csrf(), a.adminBatchDeleteInvoiceAddresses)
	g.GET("/invoice/:id", a.require("invoice.manage"), a.adminGetInvoiceRequest)
	g.POST("/invoice/:id/confirm", a.require("invoice.manage"), a.csrf(), a.adminConfirmInvoiceRequest)
	g.POST("/invoice/:id/reject", a.require("invoice.manage"), a.csrf(), a.adminRejectInvoiceRequest)
	g.POST("/invoice/:id/send", a.require("invoice.manage"), a.csrf(), a.adminSendInvoiceRequest)
	g.POST("/invoice/:id/flush", a.require("invoice.manage"), a.csrf(), a.adminFlushInvoiceRequest)
	g.POST("/invoice/:id/upload", a.require("invoice.manage"), a.csrf(), a.adminUploadInvoiceFile)
	g.GET("/invoice/:id/invoice_filename", a.require("invoice.manage"), a.adminInvoiceFile)
	g.GET("/invoice/:id/parcel_image", a.require("invoice.manage"), a.adminInvoiceParcelImage)
	g.DELETE("/invoice/:id/invoice_filename", a.require("invoice.manage"), a.csrf(), a.adminDeleteInvoiceFile)
	// 商品返现（对齐魔方 product_cashback 插件；支付成功后返到余额）
	g.GET("/product-cashbacks", a.require("product.write"), a.adminListProductCashbacks)
	g.POST("/product-cashbacks", a.require("product.write"), a.csrf(), a.adminCreateProductCashback)
	g.PUT("/product-cashbacks/:id", a.require("product.write"), a.csrf(), a.adminUpdateProductCashback)
	g.PUT("/product-cashbacks/:id/status", a.require("product.write"), a.csrf(), a.adminSetProductCashbackStatus)
	g.DELETE("/product-cashbacks/:id", a.require("product.write"), a.csrf(), a.adminDeleteProductCashback)
	// 商品购买限制（对齐魔方 product_cert_limit / product_cycle_limit / product_related_limit 插件）
	g.GET("/product-cert-limits", a.require("product.write"), a.adminListProductCertLimits)
	g.POST("/product-cert-limits", a.require("product.write"), a.csrf(), a.adminCreateProductCertLimit)
	g.PUT("/product-cert-limits/:id", a.require("product.write"), a.csrf(), a.adminUpdateProductCertLimit)
	g.PUT("/product-cert-limits/:id/status", a.require("product.write"), a.csrf(), a.adminSetProductCertLimitStatus)
	g.DELETE("/product-cert-limits/:id", a.require("product.write"), a.csrf(), a.adminDeleteProductCertLimit)
	g.GET("/product-cycle-limits", a.require("product.write"), a.adminListProductCycleLimits)
	g.POST("/product-cycle-limits", a.require("product.write"), a.csrf(), a.adminCreateProductCycleLimit)
	g.PUT("/product-cycle-limits/:id", a.require("product.write"), a.csrf(), a.adminUpdateProductCycleLimit)
	g.PUT("/product-cycle-limits/:id/status", a.require("product.write"), a.csrf(), a.adminSetProductCycleLimitStatus)
	g.DELETE("/product-cycle-limits/:id", a.require("product.write"), a.csrf(), a.adminDeleteProductCycleLimit)
	g.GET("/product-related-limits", a.require("product.write"), a.adminListProductRelatedLimits)
	g.POST("/product-related-limits", a.require("product.write"), a.csrf(), a.adminCreateProductRelatedLimit)
	g.PUT("/product-related-limits/:id", a.require("product.write"), a.csrf(), a.adminUpdateProductRelatedLimit)
	g.PUT("/product-related-limits/:id/status", a.require("product.write"), a.csrf(), a.adminSetProductRelatedLimitStatus)
	g.DELETE("/product-related-limits/:id", a.require("product.write"), a.csrf(), a.adminDeleteProductRelatedLimit)
	// 成本支出（对齐魔方 cost_pay 插件）：订单支出登记 + 自定义字段管理
	g.GET("/orders/:id/cost-pay", a.require("finance.report"), a.adminListOrderCostPays)
	g.POST("/orders/:id/cost-pay", a.require("finance.report"), a.csrf(), a.adminCreateOrderCostPay)
	g.GET("/cost-pay/summary", a.require("finance.report"), a.adminCostPaySummary)
	g.GET("/cost-pay/self-defined-field", a.require("finance.report"), a.adminListCostPayFields)
	g.POST("/cost-pay/self-defined-field", a.require("finance.report"), a.csrf(), a.adminCreateCostPayField)
	g.PUT("/cost-pay/self-defined-field/:field_id", a.require("finance.report"), a.csrf(), a.adminUpdateCostPayField)
	g.PUT("/cost-pay/self-defined-field/:field_id/show-list", a.require("finance.report"), a.csrf(), a.adminSetCostPayFieldShowList)
	g.PUT("/cost-pay/self-defined-field/:field_id/drag", a.require("finance.report"), a.csrf(), a.adminMoveCostPayField)
	g.DELETE("/cost-pay/self-defined-field/:field_id", a.require("finance.report"), a.csrf(), a.adminDeleteCostPayField)
	g.GET("/cost-pay/:id", a.require("finance.report"), a.adminGetOrderCostPay)
	g.PUT("/cost-pay/:id", a.require("finance.report"), a.csrf(), a.adminUpdateOrderCostPay)
	g.DELETE("/cost-pay/:id", a.require("finance.report"), a.csrf(), a.adminDeleteOrderCostPay)
	// 异常巡查记录（对齐魔方 abnormal_inspection_records 插件）
	g.GET("/abnormal-inspection-records", a.require("service.manage"), a.adminListInspectionRecords)
	g.POST("/abnormal-inspection-records", a.require("service.manage"), a.csrf(), a.adminCreateInspectionRecord)
	g.POST("/abnormal-inspection-records/images", a.require("service.manage"), a.csrf(), a.adminUploadInspectionImage)
	g.GET("/abnormal-inspection-records/export.xlsx", a.require("service.manage"), a.adminExportInspectionRecords)
	g.PUT("/abnormal-inspection-records/:id", a.require("service.manage"), a.csrf(), a.adminUpdateInspectionRecord)
	g.DELETE("/abnormal-inspection-records/:id", a.require("service.manage"), a.csrf(), a.adminDeleteInspectionRecord)
	g.GET("/abnormal-inspection-records/:id/images/:index", a.require("service.manage"), a.adminInspectionImage)
	// 客户自定义字段（对齐魔方 client_custom_field 插件）
	g.GET("/client-custom-fields", a.require("user.read"), a.adminListClientCustomFields)
	g.POST("/client-custom-fields", a.require("user.manage"), a.csrf(), a.adminCreateClientCustomField)
	g.PUT("/client-custom-fields/:id", a.require("user.manage"), a.csrf(), a.adminUpdateClientCustomField)
	g.PUT("/client-custom-fields/:id/status", a.require("user.manage"), a.csrf(), a.adminSetClientCustomFieldStatus)
	g.PUT("/client-custom-fields/:id/drag", a.require("user.manage"), a.csrf(), a.adminMoveClientCustomField)
	g.DELETE("/client-custom-fields/:id", a.require("user.manage"), a.csrf(), a.adminDeleteClientCustomField)
	g.GET("/users/:id/custom-fields", a.require("user.read"), a.adminUserClientFields)
	g.GET("/user-groups", a.require("agent.manage"), a.adminListUserGroups)
	g.POST("/user-groups", a.require("agent.manage"), a.csrf(), a.adminCreateUserGroup)
	g.PUT("/user-groups/:id", a.require("agent.manage"), a.csrf(), a.adminUpdateUserGroup)
	g.DELETE("/user-groups/:id", a.require("agent.manage"), a.csrf(), a.adminDeleteUserGroup)
	g.POST("/users/:id/group", a.require("agent.manage"), a.csrf(), a.adminSetUserGroup)
	g.GET("/statistics", a.require("finance.report"), a.adminStatistics)
	g.GET("/mail-templates", a.require("provider.manage"), a.adminListMailTemplates)
	g.POST("/mail-templates", a.require("provider.manage"), a.csrf(), a.adminSaveMailTemplate)
	g.DELETE("/mail-templates/:name", a.require("provider.manage"), a.csrf(), a.adminDeleteMailTemplate)
	g.GET("/email-notice-admin", a.require("email_notice.manage"), a.adminGetEmailNoticeAdmin)
	g.PUT("/email-notice-admin", a.require("email_notice.manage"), a.csrf(), a.adminSaveEmailNoticeAdmin)
	g.GET("/currencies", a.require("wallet.adjust"), a.adminListCurrencies)
	g.POST("/currencies", a.require("wallet.adjust"), a.csrf(), a.adminSaveCurrency)
	g.DELETE("/currencies/:code", a.require("wallet.adjust"), a.csrf(), a.adminDeleteCurrency)
	g.GET("/extensions", a.require("extension.manage"), a.adminListExtensions)
	g.POST("/extensions", a.require("extension.manage"), a.csrf(), a.adminUploadExtension)
	g.POST("/extensions/:id/toggle", a.require("extension.manage"), a.csrf(), a.adminSetExtensionActive)
	g.DELETE("/extensions/:id", a.require("extension.manage"), a.csrf(), a.adminDeleteExtension)
	g.GET("/extension-logs", a.require("extension.manage"), a.adminExtensionLogs)
	g.GET("/marketplace", a.require("extension.manage"), a.adminMarketplaceIndex)
	g.POST("/marketplace/install", a.require("extension.manage"), a.csrf(), a.adminMarketplaceInstall)
	g.GET("/marketplace/settings", a.require("extension.manage"), a.adminMarketplaceSettings)
	g.PUT("/marketplace/settings", a.require("extension.manage"), a.csrf(), a.adminSaveMarketplaceSettings)
	g.GET("/themes", a.require("theme.manage"), a.adminListThemes)
	g.POST("/themes", a.require("theme.manage"), a.csrf(), a.adminUploadTheme)
	g.DELETE("/themes/:id", a.require("theme.manage"), a.csrf(), a.adminDeleteTheme)
	g.GET("/settings/branding", a.require("provider.manage"), a.adminGetBranding)
	g.PUT("/settings/branding", a.require("provider.manage"), a.csrf(), a.adminSaveBranding)
	g.GET("/settings/storefront", a.require("provider.manage"), a.adminGetStorefrontSettings)
	g.PUT("/settings/storefront", a.require("provider.manage"), a.csrf(), a.adminSaveStorefrontSettings)
	g.GET("/settings/referral", a.require("wallet.adjust"), a.adminGetReferralSettings)
	g.PUT("/settings/referral", a.require("wallet.adjust"), a.csrf(), a.adminSaveReferralSettings)
	// 推介计划（对齐魔方 IdcsmartRecommend 插件）：奖励记录 / 预设无效回复 / 提现审核 / 配置
	g.GET("/recommend", a.require("wallet.adjust"), a.adminListRecommendAwards)
	g.GET("/recommend/config", a.require("wallet.adjust"), a.adminGetRecommendConfig)
	g.POST("/recommend/config", a.require("wallet.adjust"), a.csrf(), a.adminSaveRecommendConfig)
	g.PUT("/recommend/awards/:id/awards_amount", a.require("wallet.adjust"), a.csrf(), a.adminSetRecommendAwardAmount)
	g.PUT("/recommend/awards/:id/active", a.require("wallet.adjust"), a.csrf(), a.adminConfirmRecommendAward)
	g.PUT("/recommend/awards/:id/frozen", a.require("wallet.adjust"), a.csrf(), a.adminFreezeRecommendAward)
	g.PUT("/recommend/awards/:id/unfrozen", a.require("wallet.adjust"), a.csrf(), a.adminUnfreezeRecommendAward)
	g.PUT("/recommend/awards/:id/invalid", a.require("wallet.adjust"), a.csrf(), a.adminInvalidRecommendAward)
	g.DELETE("/recommend/awards/:id", a.require("wallet.adjust"), a.csrf(), a.adminDeleteRecommendAward)
	g.GET("/recommend/prereplies", a.require("wallet.adjust"), a.adminListRecommendPrereplies)
	g.POST("/recommend/prereplies", a.require("wallet.adjust"), a.csrf(), a.adminCreateRecommendPrereply)
	g.PUT("/recommend/prereplies/:id", a.require("wallet.adjust"), a.csrf(), a.adminUpdateRecommendPrereply)
	g.DELETE("/recommend/prereplies/:id", a.require("wallet.adjust"), a.csrf(), a.adminDeleteRecommendPrereply)
	g.GET("/recommend/withdrawals", a.require("wallet.adjust"), a.adminListRecommendWithdrawals)
	g.PUT("/recommend/withdrawals/:id", a.require("wallet.adjust"), a.csrf(), a.adminSetRecommendWithdrawal)
	g.GET("/export/users.csv", a.require("user.read"), a.adminExportUsers)
	g.GET("/export/orders.csv", a.require("order.read"), a.adminExportOrders)
	g.GET("/export/datasets", a.require("finance.report"), a.adminListExportDatasets)
	g.GET("/export/configs", a.require("finance.report"), a.adminListExportConfigs)
	g.POST("/export/configs", a.require("finance.report"), a.csrf(), a.adminCreateExportConfig)
	g.PUT("/export/configs/:id", a.require("finance.report"), a.csrf(), a.adminUpdateExportConfig)
	g.DELETE("/export/configs/:id", a.require("finance.report"), a.csrf(), a.adminDeleteExportConfig)
	g.GET("/export/download", a.require("finance.report"), a.adminDownloadExport)
	g.GET("/expired-ip-logs", a.require("service.manage"), a.adminListExpiredIPLogs)
	g.GET("/audit", a.require("security.audit.read"), a.adminAudit)
	g.GET("/login-logs", a.require("security.audit.read"), a.adminLoginLogs)
	g.GET("/api-logs", a.require("security.audit.read"), a.adminListAPILogs)
	g.POST("/users/:id/status", a.require("user.write"), a.csrf(), a.adminSetUserStatus)
	g.GET("/users/:id/profile", a.require("user.read"), a.adminUserProfile)
	g.GET("/services", a.require("service.manage"), a.adminListServices)
	g.POST("/services/:id/suspend", a.require("service.manage"), a.csrf(), a.adminSuspendService)
	g.POST("/services/:id/unsuspend", a.require("service.manage"), a.csrf(), a.adminUnsuspendService)
	g.POST("/services/:id/terminate", a.require("service.manage"), a.csrf(), a.adminTerminateService)
	g.POST("/orders/:id/confirm-payment", a.require("wallet.adjust"), a.csrf(), a.adminConfirmOrderPayment)
	g.POST("/orders/:id/refund", a.require("wallet.adjust"), a.csrf(), a.adminRefundOrder)
	g.GET("/refunds", a.require("wallet.adjust"), a.adminListRefunds)
	g.GET("/webhooks", a.require("webhook.manage"), a.adminListWebhooks)
	g.POST("/webhooks", a.require("webhook.manage"), a.csrf(), a.adminCreateWebhook)
	g.PUT("/webhooks/:id", a.require("webhook.manage"), a.csrf(), a.adminUpdateWebhook)
	g.DELETE("/webhooks/:id", a.require("webhook.manage"), a.csrf(), a.adminDeleteWebhook)
	g.GET("/webhooks/:id/deliveries", a.require("webhook.manage"), a.adminWebhookDeliveries)
	g.POST("/services/:id/retry", a.require("service.manage"), a.csrf(), a.adminRetryService)
	g.GET("/service-transfers", a.require("service.manage"), a.adminListServiceTransfers)
	g.POST("/service-transfers", a.require("service.manage"), a.csrf(), a.adminTransferService)
	// 流量包（对齐魔方 FlowPacket 插件）
	g.GET("/flow-packets", a.require("flow_packet.manage"), a.adminListFlowPackets)
	g.POST("/flow-packets", a.require("flow_packet.manage"), a.csrf(), a.adminCreateFlowPacket)
	g.PUT("/flow-packets/:id", a.require("flow_packet.manage"), a.csrf(), a.adminUpdateFlowPacket)
	g.PUT("/flow-packets/:id/status", a.require("flow_packet.manage"), a.csrf(), a.adminSetFlowPacketStatus)
	g.DELETE("/flow-packets/:id", a.require("flow_packet.manage"), a.csrf(), a.adminDeleteFlowPacket)
	g.GET("/flow-packet-orders", a.require("flow_packet.manage"), a.adminListFlowPacketOrders)
	g.DELETE("/flow-packet-orders/:id", a.require("flow_packet.manage"), a.csrf(), a.adminDeleteFlowPacketOrder)
	// 万云资源管理（对齐魔方 WanyunResource 插件）
	g.GET("/wanyun/fields/:scope", a.require("wanyun_resource.manage"), a.adminListWanyunFields)
	g.POST("/wanyun/fields/:scope", a.require("wanyun_resource.manage"), a.csrf(), a.adminCreateWanyunField)
	g.PUT("/wanyun/fields/:scope/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminUpdateWanyunField)
	g.PUT("/wanyun/fields/:scope/:id/show", a.require("wanyun_resource.manage"), a.csrf(), a.adminShowWanyunField)
	g.PUT("/wanyun/fields/:scope/:id/drag", a.require("wanyun_resource.manage"), a.csrf(), a.adminDragWanyunField)
	g.DELETE("/wanyun/fields/:scope/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminDeleteWanyunField)
	g.GET("/wanyun/node-types", a.require("wanyun_resource.manage"), a.adminListWanyunNodeTypes)
	g.POST("/wanyun/node-types", a.require("wanyun_resource.manage"), a.csrf(), a.adminCreateWanyunType)
	g.PUT("/wanyun/node-types/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminUpdateWanyunType)
	g.DELETE("/wanyun/node-types/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminDeleteWanyunType)
	g.GET("/wanyun/vlan-types", a.require("wanyun_resource.manage"), a.adminListWanyunVlanTypes)
	g.POST("/wanyun/vlan-types", a.require("wanyun_resource.manage"), a.csrf(), a.adminCreateWanyunType)
	g.PUT("/wanyun/vlan-types/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminUpdateWanyunType)
	g.DELETE("/wanyun/vlan-types/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminDeleteWanyunType)
	g.GET("/wanyun/nodes", a.require("wanyun_resource.manage"), a.adminListWanyunNodes)
	g.GET("/wanyun/nodes/:id", a.require("wanyun_resource.manage"), a.adminGetWanyunNode)
	g.POST("/wanyun/nodes", a.require("wanyun_resource.manage"), a.csrf(), a.adminCreateWanyunNode)
	g.PUT("/wanyun/nodes/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminUpdateWanyunNode)
	g.DELETE("/wanyun/nodes/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminDeleteWanyunNode)
	g.GET("/wanyun/vlans", a.require("wanyun_resource.manage"), a.adminListWanyunVlans)
	g.POST("/wanyun/vlans", a.require("wanyun_resource.manage"), a.csrf(), a.adminCreateWanyunVlan)
	g.PUT("/wanyun/vlans/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminUpdateWanyunVlan)
	g.PUT("/wanyun/vlans/:id/status", a.require("wanyun_resource.manage"), a.csrf(), a.adminSetWanyunVlanStatus)
	g.DELETE("/wanyun/vlans/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminDeleteWanyunVlan)
	g.GET("/wanyun/ip-segments", a.require("wanyun_resource.manage"), a.adminListWanyunIPSegments)
	g.POST("/wanyun/ip-segments", a.require("wanyun_resource.manage"), a.csrf(), a.adminCreateWanyunIPSegment)
	g.PUT("/wanyun/ip-segments/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminUpdateWanyunIPSegment)
	g.DELETE("/wanyun/ip-segments/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminDeleteWanyunIPSegment)
	g.GET("/wanyun/ip-segments/:id/addresses", a.require("wanyun_resource.manage"), a.adminListWanyunIPAddresses)
	g.POST("/wanyun/ip-segments/:id/addresses", a.require("wanyun_resource.manage"), a.csrf(), a.adminCreateWanyunIPAddress)
	g.PUT("/wanyun/ip-addresses/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminUpdateWanyunIPAddress)
	g.DELETE("/wanyun/ip-addresses/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminDeleteWanyunIPAddress)
	g.GET("/wanyun/fibers", a.require("wanyun_resource.manage"), a.adminListWanyunFibers)
	g.GET("/wanyun/fibers/:id", a.require("wanyun_resource.manage"), a.adminGetWanyunFiber)
	g.POST("/wanyun/fibers", a.require("wanyun_resource.manage"), a.csrf(), a.adminCreateWanyunFiber)
	g.PUT("/wanyun/fibers/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminUpdateWanyunFiber)
	g.DELETE("/wanyun/fibers/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminDeleteWanyunFiber)
	g.POST("/wanyun/fibers/:id/cores", a.require("wanyun_resource.manage"), a.csrf(), a.adminAddWanyunFiberCore)
	g.PUT("/wanyun/fiber-cores/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminUpdateWanyunFiberCore)
	g.DELETE("/wanyun/fiber-cores/:id", a.require("wanyun_resource.manage"), a.csrf(), a.adminDeleteWanyunFiberCore)
	// 手动资源（对齐魔方 ManualResource 插件）
	g.GET("/manual-suppliers", a.require("manual_resource.manage"), a.adminListManualSuppliers)
	g.POST("/manual-suppliers", a.require("manual_resource.manage"), a.csrf(), a.adminCreateManualSupplier)
	g.PUT("/manual-suppliers/:id", a.require("manual_resource.manage"), a.csrf(), a.adminUpdateManualSupplier)
	g.DELETE("/manual-suppliers/:id", a.require("manual_resource.manage"), a.csrf(), a.adminDeleteManualSupplier)
	g.GET("/manual-resources", a.require("manual_resource.manage"), a.adminListManualResources)
	g.GET("/manual-resources/:id", a.require("manual_resource.manage"), a.adminGetManualResource)
	g.POST("/manual-resources", a.require("manual_resource.manage"), a.csrf(), a.adminCreateManualResource)
	g.PUT("/manual-resources/:id", a.require("manual_resource.manage"), a.csrf(), a.adminUpdateManualResource)
	g.DELETE("/manual-resources/:id", a.require("manual_resource.manage"), a.csrf(), a.adminDeleteManualResource)
	g.POST("/manual-resources/:id/assign", a.require("manual_resource.manage"), a.csrf(), a.adminAssignManualResource)
	g.POST("/manual-resources/:id/idle", a.require("manual_resource.manage"), a.csrf(), a.adminIdleManualResource)
	g.POST("/manual-resources/:id/power/:action", a.require("manual_resource.manage"), a.csrf(), a.adminManualPower)
	// 业务经理（对齐魔方 IdcsmartSale 插件）
	g.GET("/sales", a.require("sale.manage"), a.adminListSales)
	g.POST("/sales", a.require("sale.manage"), a.csrf(), a.adminCreateSale)
	g.PUT("/sales/:id", a.require("sale.manage"), a.csrf(), a.adminUpdateSale)
	g.PUT("/sales/:id/status", a.require("sale.manage"), a.csrf(), a.adminSetSaleStatus)
	g.DELETE("/sales/:id", a.require("sale.manage"), a.csrf(), a.adminDeleteSale)
	g.GET("/sale-clients", a.require("sale.manage"), a.adminListSaleClients)
	g.POST("/sale-clients", a.require("sale.manage"), a.csrf(), a.adminBindSaleClient)
	g.DELETE("/sale-clients/:uid", a.require("sale.manage"), a.csrf(), a.adminUnbindSaleClient)
	g.GET("/sale-commission-configs", a.require("sale.manage"), a.adminListSaleConfigs)
	g.POST("/sale-commission-configs", a.require("sale.manage"), a.csrf(), a.adminSaveSaleConfig)
	g.DELETE("/sale-commission-configs/:id", a.require("sale.manage"), a.csrf(), a.adminDeleteSaleConfig)
	g.GET("/sale-settings", a.require("sale.manage"), a.adminGetSaleSettings)
	g.PUT("/sale-settings", a.require("sale.manage"), a.csrf(), a.adminSaveSaleSettings)
	g.GET("/sale/statistics", a.require("sale.manage"), a.adminSaleStatistics)
	g.GET("/sale/client-ranking", a.require("sale.manage"), a.adminSaleClientRanking)
	g.GET("/sale-commissions", a.require("sale.manage"), a.adminListSaleCommissions)
	g.PUT("/sale-commissions/:id/invalid", a.require("sale.manage"), a.csrf(), a.adminInvalidateSaleCommission)
	// 电子合同（对齐魔方 EContract 插件）
	g.GET("/e-contract/settings", a.require("e_contract.manage"), a.adminGetEContractSettings)
	g.PUT("/e-contract/settings", a.require("e_contract.manage"), a.csrf(), a.adminSaveEContractSettings)
	g.GET("/e-contract/templates", a.require("e_contract.manage"), a.adminListEContractTemplates)
	g.POST("/e-contract/templates", a.require("e_contract.manage"), a.csrf(), a.adminCreateEContractTemplate)
	g.PUT("/e-contract/templates/:id", a.require("e_contract.manage"), a.csrf(), a.adminUpdateEContractTemplate)
	g.POST("/e-contract/templates/:id/copy", a.require("e_contract.manage"), a.csrf(), a.adminCopyEContractTemplate)
	g.DELETE("/e-contract/templates/:id", a.require("e_contract.manage"), a.csrf(), a.adminDeleteEContractTemplate)
	g.GET("/e-contracts", a.require("e_contract.manage"), a.adminListEContracts)
	g.GET("/e-contracts/:id", a.require("e_contract.manage"), a.adminGetEContract)
	g.POST("/e-contracts/:id/review", a.require("e_contract.manage"), a.csrf(), a.adminReviewEContract)
	g.POST("/e-contracts/:id/mail", a.require("e_contract.manage"), a.csrf(), a.adminMailEContract)
	g.GET("/e-contracts/:id/download", a.require("e_contract.manage"), a.adminDownloadEContract)
	// 客户关怀（对齐魔方 ClientCare 插件）
	g.GET("/client-care", a.require("client_care.manage"), a.adminListClientCareJobs)
	g.POST("/client-care", a.require("client_care.manage"), a.csrf(), a.adminCreateClientCareJob)
	g.PUT("/client-care/:id/status", a.require("client_care.manage"), a.csrf(), a.adminSetClientCareJobStatus)
	g.DELETE("/client-care/:id", a.require("client_care.manage"), a.csrf(), a.adminDeleteClientCareJob)
	g.POST("/client-care/recipients", a.require("client_care.manage"), a.csrf(), a.adminClientCareRecipients)
	g.GET("/client-care/options", a.require("client_care.manage"), a.adminClientCareOptions)
	g.GET("/client-care/users", a.require("client_care.manage"), a.adminClientCareUsers)
	// 内部工单（对齐魔方 TicketInternalPremium 插件）
	g.GET("/ticket-internal/tickets", a.require("ticket_internal.manage"), a.adminListTicketInternal)
	g.POST("/ticket-internal/tickets", a.require("ticket_internal.manage"), a.csrf(), a.adminCreateTicketInternal)
	g.GET("/ticket-internal/tickets/:id", a.require("ticket_internal.manage"), a.adminTicketInternalDetail)
	g.PUT("/ticket-internal/tickets/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminUpdateTicketInternal)
	g.GET("/ticket-internal/tickets/:id/log", a.require("ticket_internal.manage"), a.adminTicketInternalLog)
	g.POST("/ticket-internal/tickets/:id/reply", a.require("ticket_internal.manage"), a.csrf(), a.adminReplyTicketInternal)
	g.POST("/ticket-internal/tickets/:id/accept", a.require("ticket_internal.manage"), a.csrf(), a.adminAcceptTicketInternal)
	g.POST("/ticket-internal/tickets/:id/forward", a.require("ticket_internal.manage"), a.csrf(), a.adminForwardTicketInternal)
	g.POST("/ticket-internal/tickets/:id/close", a.require("ticket_internal.manage"), a.csrf(), a.adminCloseTicketInternal)
	g.POST("/ticket-internal/tickets/:id/finish", a.require("ticket_internal.manage"), a.csrf(), a.adminFinishTicketInternal)
	g.POST("/ticket-internal/tickets/:id/score", a.require("ticket_internal.manage"), a.csrf(), a.adminScoreTicketInternal)
	g.POST("/ticket-internal/tickets/:id/notes", a.require("ticket_internal.manage"), a.csrf(), a.adminAddTicketInternalNote)
	g.PUT("/ticket-internal/notes/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminUpdateTicketInternalNote)
	g.DELETE("/ticket-internal/notes/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminDeleteTicketInternalNote)
	g.PUT("/ticket-internal/reply/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminUpdateTicketInternalReply)
	g.DELETE("/ticket-internal/reply/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminDeleteTicketInternalReply)
	g.GET("/ticket-internal/staff", a.require("ticket_internal.manage"), a.adminTicketInternalStaff)
	g.GET("/ticket-internal/hosts", a.require("ticket_internal.manage"), a.adminTicketInternalHosts)
	g.GET("/ticket-internal/config", a.require("ticket_internal.manage"), a.adminTicketInternalConfig)
	g.PUT("/ticket-internal/config", a.require("ticket_internal.manage"), a.csrf(), a.adminSaveTicketInternalConfig)
	g.GET("/ticket-internal/department", a.require("ticket_internal.manage"), a.adminTicketInternalDepartments)
	g.POST("/ticket-internal/department", a.require("ticket_internal.manage"), a.csrf(), a.adminCreateTicketInternalDepartment)
	g.PUT("/ticket-internal/department/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminUpdateTicketInternalDepartment)
	g.DELETE("/ticket-internal/department/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminDeleteTicketInternalDepartment)
	g.GET("/ticket-internal/status", a.require("ticket_internal.manage"), a.adminTicketInternalStatuses)
	g.POST("/ticket-internal/status", a.require("ticket_internal.manage"), a.csrf(), a.adminCreateTicketInternalStatus)
	g.PUT("/ticket-internal/status/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminUpdateTicketInternalStatus)
	g.DELETE("/ticket-internal/status/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminDeleteTicketInternalStatus)
	g.GET("/ticket-internal/prereply", a.require("ticket_internal.manage"), a.adminTicketInternalPrereplies)
	g.POST("/ticket-internal/prereply", a.require("ticket_internal.manage"), a.csrf(), a.adminCreateTicketInternalPrereply)
	g.PUT("/ticket-internal/prereply/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminUpdateTicketInternalPrereply)
	g.DELETE("/ticket-internal/prereply/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminDeleteTicketInternalPrereply)
	g.GET("/ticket-internal/cron", a.require("ticket_internal.manage"), a.adminTicketInternalCron)
	g.POST("/ticket-internal/cron", a.require("ticket_internal.manage"), a.csrf(), a.adminCreateTicketInternalCron)
	g.GET("/ticket-internal/cron/:id", a.require("ticket_internal.manage"), a.adminGetTicketInternalCron)
	g.PUT("/ticket-internal/cron/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminUpdateTicketInternalCron)
	g.PUT("/ticket-internal/cron/:id/status", a.require("ticket_internal.manage"), a.csrf(), a.adminSetTicketInternalCronStatus)
	g.DELETE("/ticket-internal/cron/:id", a.require("ticket_internal.manage"), a.csrf(), a.adminDeleteTicketInternalCron)
	// 用户工单高级版（对齐魔方 TicketPremium 插件）
	g.GET("/ticket-premium/tickets", a.require("ticket.manage"), a.adminTicketPremiumList)
	g.POST("/ticket-premium/tickets", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumCreate)
	g.GET("/ticket-premium/tickets/:id", a.require("ticket.manage"), a.adminTicketPremiumDetail)
	g.POST("/ticket-premium/tickets/:id/reply", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumReply)
	g.POST("/ticket-premium/tickets/:id/receive", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumAccept)
	g.POST("/ticket-premium/tickets/:id/save", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumSave)
	g.POST("/ticket-premium/tickets/:id/status", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumStatus)
	g.POST("/ticket-premium/tickets/:id/processed", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumProcessed)
	g.POST("/ticket-premium/tickets/:id/notes", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumAddNote)
	g.GET("/ticket-premium/tickets/:id/log", a.require("ticket.manage"), a.adminTicketPremiumLog)
	g.POST("/ticket-premium/tickets/:id/turn-internal", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumTurnInternal)
	g.PUT("/ticket-premium/notes/:id", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumUpdateNote)
	g.DELETE("/ticket-premium/notes/:id", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumDeleteNote)
	g.PUT("/ticket-premium/reply/:id", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumUpdateReply)
	g.DELETE("/ticket-premium/reply/:id", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumDeleteReply)
	g.GET("/ticket-premium/department", a.require("ticket.manage"), a.adminTicketPremiumDepartments)
	g.POST("/ticket-premium/department", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumDepartmentCreate)
	g.PUT("/ticket-premium/department/:id", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumDepartmentUpdate)
	g.DELETE("/ticket-premium/department/:id", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumDepartmentDelete)
	g.GET("/ticket-premium/status", a.require("ticket.manage"), a.adminTicketPremiumStatuses)
	g.POST("/ticket-premium/status", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumStatusCreate)
	g.PUT("/ticket-premium/status/:id", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumStatusUpdate)
	g.DELETE("/ticket-premium/status/:id", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumStatusDelete)
	g.GET("/ticket-premium/prereply", a.require("ticket.manage"), a.adminTicketPremiumPrereplies)
	g.POST("/ticket-premium/prereply", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumPrereplyCreate)
	g.PUT("/ticket-premium/prereply/:id", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumPrereplyUpdate)
	g.DELETE("/ticket-premium/prereply/:id", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumPrereplyDelete)
	g.GET("/ticket-premium/config", a.require("ticket.manage"), a.adminTicketPremiumConfig)
	g.PUT("/ticket-premium/config", a.require("ticket.manage"), a.csrf(), a.adminTicketPremiumSaveConfig)
	g.GET("/ticket-premium/staff", a.require("ticket.manage"), a.adminTicketPremiumStaff)
	g.GET("/ticket-premium/hosts", a.require("ticket.manage"), a.adminTicketPremiumHosts)
	g.GET("/ticket-premium/statistics", a.require("ticket.manage"), a.adminTicketPremiumStatistics)
	g.GET("/ticket-premium/rank/department_score", a.require("ticket.manage"), a.adminTicketPremiumScoreRankBy(true))
	g.GET("/ticket-premium/rank/person_score", a.require("ticket.manage"), a.adminTicketPremiumScoreRankBy(false))
	g.GET("/ticket-premium/rank/department_time", a.require("ticket.manage"), a.adminTicketPremiumTimeRankBy(true))
	g.GET("/ticket-premium/rank/person_time", a.require("ticket.manage"), a.adminTicketPremiumTimeRankBy(false))
	g.GET("/ticket-internal/statistics", a.require("ticket_internal.manage"), a.adminTicketInternalStatistics)
	g.GET("/ticket-internal/rank/department_score", a.require("ticket_internal.manage"), a.adminTicketInternalScoreRankBy(true))
	g.GET("/ticket-internal/rank/person_score", a.require("ticket_internal.manage"), a.adminTicketInternalScoreRankBy(false))
	g.GET("/ticket-internal/rank/department_time", a.require("ticket_internal.manage"), a.adminTicketInternalTimeRankBy(true))
	g.GET("/ticket-internal/rank/person_time", a.require("ticket_internal.manage"), a.adminTicketInternalTimeRankBy(false))
}

func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if id == "" {
			id = uuid.NewString()
		}
		c.Set("request_id", id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func (a *App) rateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		if a.Redis == nil || a.Cfg.RateLimitPerMinute <= 0 {
			c.Next()
			return
		}
		ip := clientIP(c)
		bucket := time.Now().UTC().Format("200601021504")
		key := "rl:" + ip + ":" + bucket
		n, err := a.Redis.Incr(c, key).Result()
		if err == nil && n == 1 {
			_ = a.Redis.Expire(c, key, 2*time.Minute).Err()
		}
		if err == nil && n > int64(a.Cfg.RateLimitPerMinute) {
			httpx.Fail(c, http.StatusTooManyRequests, "RATE_LIMITED", "请求过于频繁")
			c.Abort()
			return
		}
		c.Next()
	}
}

func (a *App) auth() gin.HandlerFunc {
	return func(c *gin.Context) {
		if strings.HasPrefix(c.GetHeader("Authorization"), "Bearer ") {
			token := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
			parts := strings.SplitN(token, ".", 2)
			if len(parts) != 2 || len(a.Cfg.MasterKey) == 0 {
				httpx.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "API Token 无效")
				c.Abort()
				return
			}
			// Per-token rate limit (第二十七阶段 API Token 权限补充).
			if a.Redis != nil && a.Cfg.Security.TokenRateLimitPerMinute > 0 {
				bucket := time.Now().UTC().Format("200601021504")
				key := "rl:token:" + parts[0] + ":" + bucket
				n, err := a.Redis.Incr(c, key).Result()
				if err == nil && n == 1 {
					_ = a.Redis.Expire(c, key, 2*time.Minute).Err()
				}
				if err == nil && n > int64(a.Cfg.Security.TokenRateLimitPerMinute) {
					httpx.Fail(c, http.StatusTooManyRequests, "RATE_LIMITED", "API Token 请求过于频繁")
					c.Abort()
					return
				}
			}
			hash, err := security.HMACSecret(a.Cfg.MasterKey, parts[1])
			if err != nil {
				httpx.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "API Token 无效")
				c.Abort()
				return
			}
			u, scopes, err := a.Store.AuthenticateAPIToken(c, parts[0], hash, clientIP(c))
			if err != nil {
				httpx.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "API Token 无效")
				c.Abort()
				return
			}
			perms := map[string]bool{}
			for _, s := range scopes {
				perms[s] = true
			}
			c.Set(principalKey, principal{User: u, Permissions: perms, APIToken: true})
			c.Next()
			return
		}
		cookie, err := c.Cookie("shitidc_session")
		if err != nil || cookie == "" {
			httpx.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "请先登录")
			c.Abort()
			return
		}
		u, csrf, err := a.Store.SessionUser(c, security.SHA256Hex(cookie))
		if err != nil {
			httpx.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "登录已失效")
			c.Abort()
			return
		}
		perms, err := a.Store.Permissions(c, u.ID)
		if err != nil {
			httpx.Fail(c, http.StatusInternalServerError, "INTERNAL_ERROR", "权限读取失败")
			c.Abort()
			return
		}
		c.Set(principalKey, principal{User: u, Permissions: perms, CSRF: csrf})
		c.Next()
	}
}

func (a *App) csrf() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := getPrincipal(c)
		if !ok {
			return
		}
		if p.APIToken {
			c.Next()
			return
		}
		if c.GetHeader("X-CSRF-Token") == "" || c.GetHeader("X-CSRF-Token") != p.CSRF {
			httpx.Fail(c, http.StatusForbidden, "CSRF_INVALID", "CSRF Token 无效")
			c.Abort()
			return
		}
		c.Next()
	}
}
func (a *App) require(permission string) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, ok := getPrincipal(c)
		if !ok {
			httpx.Fail(c, http.StatusUnauthorized, "UNAUTHORIZED", "请先登录")
			c.Abort()
			return
		}
		if !p.Permissions[permission] {
			httpx.Fail(c, http.StatusForbidden, "FORBIDDEN", "没有权限")
			c.Abort()
			return
		}
		c.Next()
	}
}
func getPrincipal(c *gin.Context) (principal, bool) {
	v, ok := c.Get(principalKey)
	if !ok {
		return principal{}, false
	}
	p, ok := v.(principal)
	return p, ok
}
func clientIP(c *gin.Context) string {
	ip := c.ClientIP()
	if parsed := net.ParseIP(ip); parsed != nil {
		return parsed.String()
	}
	return ""
}

// renderMail renders an admin-editable template when one exists, otherwise
// falls back to the built-in subject/body pair (§17 邮件模板).
func (a *App) renderMail(name, fallbackSubject, fallbackBody string, vars map[string]string) (string, string) {
	tpl, err := a.Store.GetMailTemplate(context.Background(), name)
	if err != nil {
		return fallbackSubject, fallbackBody
	}
	if tpl.Subject == "" && tpl.Body == "" {
		return fallbackSubject, fallbackBody
	}
	subject := tpl.Subject
	if subject == "" {
		subject = fallbackSubject
	}
	return mail.RenderTemplate(subject, vars), mail.RenderTemplate(tpl.Body, vars)
}

func (a *App) health(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c, 2*time.Second)
	defer cancel()
	if err := a.Store.DB.Ping(ctx); err != nil {
		httpx.Fail(c, 503, "DB_UNAVAILABLE", "database unavailable")
		return
	}
	httpx.OK(c, 200, map[string]string{"status": "ok"})
}

func (a *App) authConfig(c *gin.Context) {
	mailSettings, err := a.Store.GetMailSettings(c)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取邮件配置失败")
		return
	}
	// 「邮件服务已配置」= 启用中的邮件通道或内置 SMTP 任一可用。
	mailEnabled := a.mailConfigured(c)
	httpx.OK(c, 200, map[string]bool{
		"email_verify_required": mailSettings.VerifyRequired && mailEnabled,
		"smtp_enabled":          mailEnabled,
		"captcha_enabled":       a.captchaEnabled(c),
	})
}

func (a *App) register(c *gin.Context) {
	var in struct {
		Email          string            `json:"email"`
		Password       string            `json:"password"`
		Code           string            `json:"code"`
		ReferralCode   string            `json:"referral_code"`
		CaptchaID      string            `json:"captcha_id"`
		CaptchaAns     string            `json:"captcha_answer"`
		CaptchaToken   string            `json:"captcha_token"`
		CaptchaRandstr string            `json:"captcha_randstr"`
		CustomFields   map[string]string `json:"custom_fields"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if !a.verifyCaptcha(c, in.CaptchaID, in.CaptchaAns, in.CaptchaToken, in.CaptchaRandstr) {
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if !strings.Contains(in.Email, "@") {
		httpx.Fail(c, 400, "INVALID_EMAIL", "邮箱格式错误")
		return
	}
	// 客户自定义字段：先校验，避免账号建出来才发现字段不合法。
	if err := a.Store.ValidateRegisterFieldValues(c, in.CustomFields); err != nil {
		httpx.Fail(c, 400, "CUSTOM_FIELD_INVALID", err.Error())
		return
	}
	mailSettings, err := a.Store.GetMailSettings(c)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取邮件配置失败")
		return
	}
	mailEnabled := a.mailConfigured(c)
	hash, err := security.HashPassword(in.Password)
	if err != nil {
		httpx.Fail(c, 400, "WEAK_PASSWORD", err.Error())
		return
	}
	// 邮箱验证码：未开启“强制邮箱验证”时它是可选的——填了就必须正确，填对了直接
	// 把账号标记为已验证（省掉登录后再验证一次）；不填则照常注册，登录时再验证。
	verified := false
	code := strings.TrimSpace(in.Code)
	switch {
	case mailSettings.VerifyRequired && mailEnabled:
		if len(code) != 6 {
			httpx.Fail(c, 400, "CODE_REQUIRED", "请输入邮箱验证码")
			return
		}
		if err := a.Store.ConsumeEmailCode(c, in.Email, "register", security.SHA256Hex(code)); err != nil {
			httpx.Fail(c, 400, mapCodeError(err), "验证码错误或已过期")
			return
		}
		verified = true
	case code != "":
		if !mailEnabled {
			httpx.Fail(c, 400, "SMTP_NOT_CONFIGURED", "邮件服务未配置，无法校验邮箱验证码")
			return
		}
		if len(code) != 6 {
			httpx.Fail(c, 400, "CODE_INVALID", "邮箱验证码为 6 位数字")
			return
		}
		if err := a.Store.ConsumeEmailCode(c, in.Email, "register", security.SHA256Hex(code)); err != nil {
			httpx.Fail(c, 400, mapCodeError(err), "验证码错误或已过期")
			return
		}
		verified = true
	}
	u, err := a.Store.CreateUser(c, in.Email, hash, verified)
	if err != nil {
		httpx.Fail(c, 409, "USER_EXISTS", "用户已存在或数据冲突")
		return
	}
	// 注册时填写的客户自定义字段（失败不阻断注册，用户可在个人中心补填）。
	if len(in.CustomFields) > 0 {
		if err := a.Store.SaveRegisterFieldValues(c, u.ID, in.CustomFields); err != nil {
			slog.Warn("register custom fields save failed", "error", err)
		}
	}
	// 推广系统: attribute the fresh registration to the invite code owner.
	if in.ReferralCode != "" {
		if _, rerr := a.Store.ApplyReferral(c, u.ID, in.ReferralCode); rerr != nil {
			slog.Warn("referral apply failed", "error", rerr)
		}
	}
	_ = a.Store.Audit(c, u.ID, "auth.register", "user", u.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	a.Bus.Emit(a.eventCtx(c), events.UserRegistered, map[string]any{"user_id": u.PublicID, "uid": u.ID, "email": security.MaskEmail(u.Email)})
	httpx.OK(c, 201, u)
}

func (a *App) sendEmailCode(c *gin.Context) {
	var in struct {
		Email          string            `json:"email"`
		CaptchaID      string            `json:"captcha_id"`
		CaptchaAns     string            `json:"captcha_answer"`
		CaptchaToken   string            `json:"captcha_token"`
		CaptchaRandstr string            `json:"captcha_randstr"`
		CustomFields   map[string]string `json:"custom_fields"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if !a.verifyCaptcha(c, in.CaptchaID, in.CaptchaAns, in.CaptchaToken, in.CaptchaRandstr) {
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	if !strings.Contains(in.Email, "@") {
		httpx.Fail(c, 400, "INVALID_EMAIL", "邮箱格式错误")
		return
	}
	// 客户自定义字段：先校验，避免账号建出来才发现字段不合法。
	if err := a.Store.ValidateRegisterFieldValues(c, in.CustomFields); err != nil {
		httpx.Fail(c, 400, "CUSTOM_FIELD_INVALID", err.Error())
		return
	}
	// 发送前先解析出可用的发信方式（通道优先，SMTP 兜底），
	// 未配置时直接拒绝，绝不把验证码写进库再让邮件发不出去。
	sender, _, err := a.resolveMailSender(c)
	if err != nil {
		httpx.Fail(c, 503, "SMTP_NOT_CONFIGURED", "邮件服务未配置，请联系管理员在后台设置邮件通道或 SMTP")
		return
	}
	code, err := security.NumericCode(6)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成验证码失败")
		return
	}
	if err := a.Store.PutEmailCode(c, in.Email, "register", security.SHA256Hex(code), 10*time.Minute, 60*time.Second); err != nil {
		if errors.Is(err, store.ErrCodeRateLimited) {
			httpx.Fail(c, 429, "CODE_RATE_LIMITED", "发送太频繁，请 1 分钟后再试")
			return
		}
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存验证码失败")
		return
	}
	verSubject, verBody := mail.VerificationMail(code)
	subject, body := a.renderMail("email_verification", verSubject, verBody, map[string]string{"code": code, "email": in.Email})
	if a.Queue != nil {
		// 第八阶段: email sending leaves the request path (mail.send task).
		if err := a.Queue.MailSend(in.Email, subject, body); err != nil {
			httpx.Fail(c, 502, "MAIL_ENQUEUE_FAILED", "验证码邮件投递失败，请稍后重试")
			return
		}
	} else {
		ctx, cancel := context.WithTimeout(c, 20*time.Second)
		defer cancel()
		if err := sender(ctx, in.Email, subject, body); err != nil {
			httpx.Fail(c, 502, "MAIL_SEND_FAILED", "验证码邮件发送失败: "+err.Error())
			return
		}
	}
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) verifyEmail(c *gin.Context) {
	var in struct {
		Email string `json:"email"`
		Code  string `json:"code"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Email = strings.TrimSpace(strings.ToLower(in.Email))
	code := strings.TrimSpace(in.Code)
	if !strings.Contains(in.Email, "@") || len(code) != 6 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "邮箱或验证码格式错误")
		return
	}
	if err := a.Store.ConsumeEmailCode(c, in.Email, "register", security.SHA256Hex(code)); err != nil {
		httpx.Fail(c, 400, mapCodeError(err), "验证码错误或已过期")
		return
	}
	u, _, err := a.Store.GetUserByEmail(c, in.Email)
	if err != nil {
		httpx.Fail(c, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	if err := a.Store.SetEmailVerified(c, u.ID, true); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "验证状态更新失败")
		return
	}
	_ = a.Store.Audit(c, u.ID, "user.email.verified", "user", u.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func mapCodeError(err error) string {
	switch {
	case errors.Is(err, store.ErrCodeExpired):
		return "CODE_EXPIRED"
	case errors.Is(err, store.ErrCodeLocked):
		return "CODE_LOCKED"
	case errors.Is(err, store.ErrCodeRateLimited):
		return "CODE_RATE_LIMITED"
	default:
		return "CODE_INVALID"
	}
}

// eventCtx returns a request context tagged with the request id so emitted
// events carry it through to webhook deliveries.
func (a *App) eventCtx(c *gin.Context) context.Context {
	return events.WithRequestID(c.Request.Context(), c.GetString("request_id"))
}

func (a *App) version(c *gin.Context) {
	info, ok := debug.ReadBuildInfo()
	rev, date := "dev", ""
	if ok {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.time":
				date = s.Value
			}
		}
	}
	httpx.OK(c, 200, map[string]string{"version": rev, "build_time": date})
}

func (a *App) login(c *gin.Context) {
	var in struct {
		Email          string `json:"email"`
		Password       string `json:"password"`
		TotpCode       string `json:"totp_code"`
		CaptchaID      string `json:"captcha_id"`
		CaptchaAns     string `json:"captcha_answer"`
		CaptchaToken   string `json:"captcha_token"`
		CaptchaRandstr string `json:"captcha_randstr"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if !a.verifyCaptcha(c, in.CaptchaID, in.CaptchaAns, in.CaptchaToken, in.CaptchaRandstr) {
		return
	}
	email := strings.TrimSpace(strings.ToLower(in.Email))
	ip, ua := clientIP(c), c.Request.UserAgent()
	failLogin := func(reason string) {
		_ = a.Store.RecordLoginAttempt(c, 0, email, false, reason, ip, ua)
		time.Sleep(120 * time.Millisecond)
		httpx.Fail(c, 401, "INVALID_CREDENTIALS", "邮箱或密码错误")
	}
	fail2FA := func(reason, code, message string) {
		_ = a.Store.RecordLoginAttempt(c, 0, email, false, reason, ip, ua)
		time.Sleep(120 * time.Millisecond)
		httpx.Fail(c, 401, code, message)
	}
	loginKey := "login_fail:" + security.SHA256Hex(email)
	if a.Redis != nil {
		if n, _ := a.Redis.Get(c, loginKey).Int(); n >= 10 {
			_ = a.Store.SecurityEvent(c, 0, "login.rate_limited", "warning", ip, map[string]any{"email": security.MaskEmail(email)})
			_ = a.Store.RecordLoginAttempt(c, 0, email, false, "rate_limited", ip, ua)
			httpx.Fail(c, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "登录失败次数过多，请稍后再试")
			return
		}
	}
	creds, err := a.Store.GetLoginCredentials(c, email)
	if errors.Is(err, store.ErrNotFound) {
		failLogin("unknown_email")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取账户失败")
		return
	}
	if creds.LockedUntil != nil && creds.LockedUntil.After(time.Now()) {
		_ = a.Store.RecordLoginAttempt(c, creds.User.ID, email, false, "account_locked", ip, ua)
		httpx.Fail(c, http.StatusLocked, "ACCOUNT_LOCKED", "失败次数过多，账户已临时锁定，请稍后再试或重置密码")
		return
	}
	ok := security.VerifyPassword(creds.PasswordHash, in.Password)
	legacyOK := false
	if !ok && creds.LegacyHash != "" && security.VerifyLegacyPassword(creds.LegacyHash, in.Password) {
		// Migrated MagicCube account: verify the legacy md5 hash once, then
		// upgrade to Argon2id (第十四阶段 §37 PasswordVerifier).
		ok, legacyOK = true, true
	}
	if !ok {
		if a.Redis != nil {
			n, _ := a.Redis.Incr(c, loginKey).Result()
			if n == 1 {
				_ = a.Redis.Expire(c, loginKey, 15*time.Minute).Err()
			}
		}
		_ = a.Store.RegisterLoginFailure(c, email, 10, 15)
		_ = a.Store.RecordLoginAttempt(c, creds.User.ID, email, false, "bad_password", ip, ua)
		if creds.FailedAttempts+1 == 10 {
			_ = a.Store.SecurityEvent(c, creds.User.ID, "login.locked", "warning", ip, map[string]any{"email": security.MaskEmail(email)})
		}
		failLogin("bad_password")
		return
	}
	if creds.User.Status != "active" {
		_ = a.Store.RecordLoginAttempt(c, creds.User.ID, email, false, "account_disabled", ip, ua)
		httpx.Fail(c, 403, "ACCOUNT_DISABLED", "账户不可用")
		return
	}
	if settings, serr := a.Store.GetMailSettings(c); serr == nil {
		if settings.VerifyRequired && a.mailConfigured(c) && !creds.User.EmailVerified {
			httpx.Fail(c, 403, "EMAIL_NOT_VERIFIED", "邮箱未验证，请先通过验证码完成验证")
			return
		}
	}
	// TOTP second factor (§9 可选 2FA): the 6-digit code is required once the
	// factor is enabled; failures share the brute-force budget with passwords.
	if creds.TOTPEnabled {
		if len(a.Cfg.MasterKey) != 32 {
			httpx.Fail(c, 500, "MASTER_KEY_REQUIRED", "服务器缺少 MASTER_KEY_BASE64，无法校验两步验证")
			return
		}
		secret, derr := security.Decrypt(a.Cfg.MasterKey, creds.TOTPSecretEnc)
		if derr != nil {
			fail2FA("totp_decrypt_failed", "TOTP_INVALID", "两步验证配置损坏，请联系管理员")
			return
		}
		if strings.TrimSpace(in.TotpCode) == "" {
			fail2FA("totp_missing", "TOTP_REQUIRED", "请输入两步验证码")
			return
		}
		if !security.VerifyTOTP(secret, in.TotpCode) {
			if a.Redis != nil {
				n, _ := a.Redis.Incr(c, loginKey).Result()
				if n == 1 {
					_ = a.Redis.Expire(c, loginKey, 15*time.Minute).Err()
				}
			}
			_ = a.Store.RegisterLoginFailure(c, email, 10, 15)
			fail2FA("totp_failed", "TOTP_INVALID", "两步验证码错误")
			return
		}
	}
	// 旧版哈希（从魔方迁移过来的账号）在验证通过后顺手升级成 bcrypt。
	if legacyOK {
		if newHash, herr := security.HashPassword(in.Password); herr == nil {
			_ = a.Store.UpgradeLegacyPassword(c, creds.User.ID, newHash)
		}
	}
	if a.Redis != nil {
		_ = a.Redis.Del(c, loginKey).Err()
	}
	_ = a.Store.ClearLoginFailures(c, creds.User.ID)
	token, _ := security.RandomToken(32)
	csrf, _ := security.RandomToken(24)
	expires := time.Now().Add(a.Cfg.SessionTTL)
	if err := a.Store.CreateSession(c, creds.User.ID, security.SHA256Hex(token), csrf, net.ParseIP(ip), ua, expires); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "创建会话失败")
		return
	}
	_ = a.Store.RecordLoginAttempt(c, creds.User.ID, email, true, "ok", ip, ua)
	_ = a.Store.Audit(c, creds.User.ID, "auth.login", "user", creds.User.PublicID, c.GetString("request_id"), ip, ua, nil, nil)
	// 异常 IP 检测：本账号从未用过的地址登录成功时记一条安全事件。
	if newIP, err := a.Store.HasSuccessLoginFromOtherIP(c, creds.User.ID, ip); err == nil && newIP {
		_ = a.Store.SecurityEvent(c, creds.User.ID, "login.new_ip", "notice", ip, map[string]any{"email": security.MaskEmail(email)})
	}
	a.Bus.Emit(a.eventCtx(c), events.UserLogin, map[string]any{"user_id": creds.User.PublicID, "uid": creds.User.ID, "ip": ip})
	if a.Cfg.Mail.LoginNotify && a.Queue != nil {
		subject, body := a.renderMail("login_notify", "ShitIDC 登录提醒", "<p>你的账号刚刚完成了一次登录。</p><p>IP: "+ip+"</p><p>如果不是你本人操作，请立即修改密码。</p>", map[string]string{"ip": ip, "email": creds.User.Email})
		_ = a.Queue.MailSend(creds.User.Email, subject, body)
	}
	c.SetSameSite(http.SameSiteLaxMode)
	c.SetCookie("shitidc_session", token, int(a.Cfg.SessionTTL.Seconds()), "/", a.Cfg.CookieDomain, a.Cfg.CookieSecure, true)
	httpx.OK(c, 200, map[string]any{"user": creds.User, "csrf_token": csrf})
}
