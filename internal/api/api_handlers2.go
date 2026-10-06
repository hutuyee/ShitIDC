package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/provider/magiccube"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 本文件补齐 api.go 里注册但需要 store 层的处理函数：
// 用户侧的钱包/发票/机器/工单，以及后台的商品创建与魔方连通性测试。

// firstNonEmptyAPI 返回第一个非空字符串（兼容接口里大量使用）。
func firstNonEmptyAPI(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// ---- 用户侧 ----

// listInvoices 返回当前用户的账单。
func (a *App) listInvoices(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.ListInvoices(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取账单失败")
		return
	}
	httpx.OK(c, 200, v)
}

// wallet 返回某个币种的钱包余额，缺省 CNY。
func (a *App) wallet(c *gin.Context) {
	p, _ := getPrincipal(c)
	currency := strings.ToUpper(strings.TrimSpace(c.Query("currency")))
	if currency == "" {
		currency = "CNY"
	}
	w, err := a.Store.Wallet(c, p.User.ID, currency)
	if errors.Is(err, store.ErrNotFound) {
		// 该币种还没有钱包记录时返回 0 而不是 404：前端少一个分支。
		httpx.OK(c, 200, map[string]any{"currency": currency, "balance_cents": 0})
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取钱包失败")
		return
	}
	httpx.OK(c, 200, w)
}

// walletTransactions 返回钱包流水。
func (a *App) walletTransactions(c *gin.Context) {
	p, _ := getPrincipal(c)
	currency := strings.ToUpper(strings.TrimSpace(c.Query("currency")))
	if currency == "" {
		currency = "CNY"
	}
	v, err := a.Store.WalletTransactions(c, p.User.ID, currency)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取流水失败")
		return
	}
	httpx.OK(c, 200, v)
}

// listServices 返回当前用户的服务；带 id 时返回单台详情。
func (a *App) listServices(c *gin.Context) {
	p, _ := getPrincipal(c)
	if id := strings.TrimSpace(c.Param("id")); id != "" {
		d, err := a.Store.GetServiceDetail(c, p.User.ID, id)
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "SERVICE_NOT_FOUND", "服务不存在")
			return
		}
		if err != nil {
			httpx.Fail(c, 500, "INTERNAL_ERROR", "读取服务失败")
			return
		}
		httpx.OK(c, 200, d)
		return
	}
	v, err := a.Store.ListServices(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取服务失败")
		return
	}
	httpx.OK(c, 200, v)
}

// listTickets 返回当前用户的工单（高级版：含编号 / 状态 / 部门类型 / 处理时限）。
func (a *App) listTickets(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, _, err := a.Store.ListTicketsPremium(c, store.TicketPremiumFilter{ClientID: p.User.ID, Limit: 200})
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单失败")
		return
	}
	httpx.OK(c, 200, v)
}

// createTicket 新建工单（高级版：部门 / 类型 / 关联产品 / 附件）并通知客服邮箱。
func (a *App) createTicket(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Subject      string   `json:"subject"`
		Priority     string   `json:"priority"`
		Message      string   `json:"message"`
		DepartmentID int64    `json:"department_id"`
		TicketTypeID int64    `json:"ticket_type_id"`
		HostIDs      []int64  `json:"host_ids"`
		Attachment   []string `json:"attachment"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if in.Priority == "" {
		in.Priority = "normal"
	}
	subject := strings.TrimSpace(in.Subject)
	body := strings.TrimSpace(in.Message)
	if len([]rune(subject)) < 2 || len([]rune(body)) < 2 {
		httpx.Fail(c, 400, "INVALID_TICKET", "工单标题与内容都不能少于 2 个字")
		return
	}
	v, err := a.Store.CreateTicketPremium(c, store.TicketPremiumCreateInput{
		UserID:        p.User.ID,
		DepartmentID:  in.DepartmentID,
		TypeID:        in.TicketTypeID,
		Title:         subject,
		Priority:      in.Priority,
		HostIDs:       in.HostIDs,
		Message:       body,
		AttachmentIDs: in.Attachment,
	})
	if err != nil {
		httpx.Fail(c, 400, "TICKET_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.create", "ticket", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	// 通知客服；失败不影响工单本身。
	a.mailTicketNotification(c, v.Subject, v.PublicID, false, p.User.Email, body)
	httpx.OK(c, 201, v)
}

// ---- 兼容接口：下游把 ShitIDC 当上游时拉商品列表 ----

// compatProducts 返回商品列表。
//
// 形状沿用 upstreamProductRow：魔方客户端解析商品时是**递归扫描**所有层级里
// 同时有 id 与 name 的对象，所以这里只要保证每行带上这两个字段就一定能被识别。
func (a *App) compatProducts(c *gin.Context) {
	products, err := a.Store.ListProducts(c)
	if err != nil {
		compatFail(c, http.StatusInternalServerError, "读取商品失败")
		return
	}
	rows := make([]upstreamProductRow, 0, len(products))
	for _, prod := range products {
		cycles := []string{prod.BillingCycle}
		if prices, perr := a.Store.ListProductPrices(c, prod.PublicID); perr == nil && len(prices) > 0 {
			cycles = cycles[:0]
			for _, pr := range prices {
				cycles = append(cycles, pr.BillingCycle)
			}
		}
		rows = append(rows, upstreamProductRow{
			ID:           prod.PublicID,
			Name:         prod.Name,
			Description:  prod.Description,
			PriceCents:   prod.PriceCents,
			Price:        formatCents(prod.PriceCents),
			Currency:     prod.Currency,
			BillingCycle: prod.BillingCycle,
			Cycles:       cycles,
			Group:        prod.GroupName,
			Stock:        -1,
		})
	}
	httpx.OK(c, 200, gin.H{"list": rows, "total": len(rows)})
}

// ---- 后台：商品创建 ----

// adminCreateProduct 新建商品。
//
// 与 adminUpdateProduct 用同一套字段，所以前端编辑与新建可以共用一个表单。
func (a *App) adminCreateProduct(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name              string `json:"name"`
		Description       string `json:"description"`
		GroupID           string `json:"group_id"`
		ProviderID        string `json:"provider_id"`
		ProviderType      string `json:"provider_type"`
		Currency          string `json:"currency"`
		PayType           string `json:"pay_type"`
		TrialDays         int    `json:"trial_days"`
		TrialPriceCents   int64  `json:"trial_price_cents"`
		AutoTerminateDays int    `json:"auto_terminate_days"`
		StockControl      bool   `json:"stock_control"`
		StockQty          int    `json:"stock_qty"`
		AllowQty          bool   `json:"allow_qty"`
		MaxPerCustomer    int    `json:"max_per_customer"`
		IsFeatured        bool   `json:"is_featured"`
		Prices            []struct {
			BillingCycle string `json:"billing_cycle"`
			AmountCents  int64  `json:"amount_cents"`
			Currency     string `json:"currency"`
		} `json:"prices"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写商品名称")
		return
	}
	currency := strings.ToUpper(strings.TrimSpace(in.Currency))
	if currency == "" {
		currency = "CNY"
	}
	providerType := strings.ToLower(strings.TrimSpace(in.ProviderType))
	if providerType == "" {
		providerType = "manual"
	}
	// 价格档：按币种分组，未提交的周期在该币种内下架。
	prices := make([]store.PriceInput, 0, len(in.Prices))
	for _, pr := range in.Prices {
		cycle := strings.TrimSpace(pr.BillingCycle)
		if cycle == "" {
			cycle = "monthly"
		}
		prices = append(prices, store.PriceInput{BillingCycle: cycle, AmountCents: pr.AmountCents, Currency: pr.Currency})
	}
	if len(prices) == 0 {
		prices = []store.PriceInput{{BillingCycle: "monthly", AmountCents: 0}}
	}
	v, err := a.Store.CreateProductWithPrices(c, strings.TrimSpace(in.Name), in.Description,
		providerType, strings.TrimSpace(in.ProviderID), "", currency, strings.TrimSpace(in.GroupID),
		store.ProductBillingInput{
			PayType:           in.PayType,
			TrialDays:         in.TrialDays,
			TrialPriceCents:   in.TrialPriceCents,
			AutoTerminateDays: in.AutoTerminateDays,
			StockControl:      in.StockControl,
			StockQty:          in.StockQty,
			AllowQty:          in.AllowQty,
			MaxPerCustomer:    in.MaxPerCustomer,
			IsFeatured:        in.IsFeatured,
		}, prices)
	if err != nil {
		httpx.Fail(c, 400, "PRODUCT_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "product.create", "product", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": v.Name, "currency": currency})
	httpx.OK(c, 201, v)
}

// testMagicCube 测试一组魔方接入参数是否可用（落库前先试）。
func (a *App) testMagicCube(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		BaseURL     string `json:"base_url"`
		Username    string `json:"username"`
		APIKey      string `json:"api_key"`
		AuthMode    string `json:"auth_mode"`
		TokenPrefix string `json:"token_prefix"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.BaseURL) == "" || strings.TrimSpace(in.APIKey) == "" {
		httpx.Fail(c, 400, "PROVIDER_CONFIG_INVALID", "接口地址与 API Key 不能为空")
		return
	}
	client, err := magiccube.New(magiccube.Config{
		BaseURL:     strings.TrimSpace(in.BaseURL),
		Username:    strings.TrimSpace(in.Username),
		APIKey:      in.APIKey,
		AuthMode:    in.AuthMode,
		TokenPrefix: in.TokenPrefix,
	})
	if err != nil {
		httpx.Fail(c, 400, "PROVIDER_CONFIG_INVALID", err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(c, providerTestTimeout)
	defer cancel()
	if err := client.TestConnection(ctx); err != nil {
		httpx.OK(c, 200, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "provider.magiccube.test", "provider", "", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"base_url": in.BaseURL})
	httpx.OK(c, 200, map[string]any{"ok": true, "message": "连接正常"})
}

var _ = time.Now
