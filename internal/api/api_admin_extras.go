package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/model"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// ---- service lifecycle console ----

func (a *App) adminListServices(c *gin.Context) {
	v, err := a.Store.ListServicesAdmin(c, strings.TrimSpace(c.Query("status")), parseIntDefault(c.Query("limit"), 200))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取服务失败")
		return
	}
	httpx.OK(c, 200, v)
}

// lifecycleHandler claims the transition locally and hands the provider call
// to the worker. Returns 409 when the service is not in a valid source state.
func (a *App) lifecycleHandler(c *gin.Context, action string) {
	p, _ := getPrincipal(c)
	var claimed bool
	var err error
	switch action {
	case "suspend":
		_, _, claimed, err = a.Store.ClaimServiceForTransition(c, c.Param("id"), "suspending", "active")
	case "unsuspend":
		_, _, claimed, err = a.Store.ClaimServiceForTransition(c, c.Param("id"), "unsuspending", "suspended")
	case "terminate":
		_, _, claimed, err = a.Store.ClaimServiceForTransition(c, c.Param("id"), "terminating", "active", "suspended")
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "服务状态更新失败")
		return
	}
	if !claimed {
		httpx.Fail(c, 409, "SERVICE_INVALID_STATE", "服务当前状态不支持该操作")
		return
	}
	if a.Queue == nil {
		// No queue: roll back so the service is not stuck in transition.
		_ = a.Store.FinalizeServiceTransition(c, c.Param("id"), "", false, "queue unavailable")
		httpx.Fail(c, 503, "QUEUE_UNAVAILABLE", "任务队列不可用")
		return
	}
	switch action {
	case "suspend":
		err = a.Queue.ServiceSuspend(c.Param("id"))
	case "unsuspend":
		err = a.Queue.ServiceUnsuspend(c.Param("id"))
	case "terminate":
		err = a.Queue.ServiceTerminate(c.Param("id"))
	}
	if err != nil {
		httpx.Fail(c, 502, "QUEUE_ENQUEUE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "service."+action, "service", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"action": action})
	httpx.OK(c, 202, map[string]any{"status": "accepted", "action": action, "service_id": c.Param("id")})
}

func (a *App) adminSuspendService(c *gin.Context)   { a.lifecycleHandler(c, "suspend") }
func (a *App) adminUnsuspendService(c *gin.Context) { a.lifecycleHandler(c, "unsuspend") }
func (a *App) adminTerminateService(c *gin.Context) { a.lifecycleHandler(c, "terminate") }

// ---- refunds (冲正) ----

func (a *App) adminRefundOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.Reason) == "" {
		httpx.Fail(c, 400, "REASON_REQUIRED", "必须填写退款原因")
		return
	}
	userID, err := a.Store.GetOrderUser(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "ORDER_NOT_FOUND", "订单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取订单失败")
		return
	}
	// Gateway refund dispatch (自动退款): online payments with a Refunder
	// gateway are refunded at the gateway; wallet payments use 冲正.
	info, perr := a.Store.GetOrderPaymentInfo(c, c.Param("id"))
	if perr == nil && !strings.EqualFold(info.Method, "wallet") {
		r, gerr := a.gatewayRefund(c, p.User.ID, c.Param("id"), strings.TrimSpace(in.Reason), info)
		if gerr != nil {
			return // response already written
		}
		httpx.OK(c, 201, r)
		return
	}
	r, err := a.Store.RefundOrder(c, p.User.ID, userID, c.Param("id"), strings.TrimSpace(in.Reason), c.GetString("request_id"))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "ORDER_NOT_FOUND", "订单不存在")
		return
	case errors.Is(err, store.ErrNotRefundable):
		httpx.Fail(c, 409, "ORDER_NOT_REFUNDABLE", "订单当前状态不可退款（仅钱包支付且已完成的订单支持）")
		return
	case err != nil:
		httpx.Fail(c, 400, "REFUND_FAILED", err.Error())
		return
	}
	a.Bus.Emit(a.eventCtx(c), events.OrderRefunded, map[string]any{"order_id": r.OrderID, "user_id": userID, "amount_cents": r.AmountCents, "refund_id": r.PublicID})
	httpx.OK(c, 201, r)
}

func (a *App) adminListRefunds(c *gin.Context) {
	v, err := a.Store.ListRefunds(c, parseIntDefault(c.Query("limit"), 100))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取退款记录失败")
		return
	}
	httpx.OK(c, 200, v)
}

// ---- outbound webhooks ----

func sanitizeWebhookEvents(raw []string) []string {
	allowed := map[string]bool{
		events.UserRegistered: true, events.UserLogin: true, events.UserPasswordReset: true,
		events.OrderCreated: true, events.OrderPaid: true, events.OrderCancelled: true, events.OrderRefunded: true,
		events.InvoicePaid: true, events.WalletRecharged: true, events.WalletAdjusted: true, events.PaymentFailed: true,
		events.ServiceCreated: true, events.ServiceFailed: true, events.ServiceRenewed: true,
		events.ServiceSuspended: true, events.ServiceUnsuspended: true, events.ServiceTerminated: true,
		events.TicketCreated: true, events.TicketReplied: true,
	}
	out := []string{}
	for _, e := range raw {
		if allowed[strings.TrimSpace(e)] {
			out = append(out, strings.TrimSpace(e))
		}
	}
	return out
}

func (a *App) adminListWebhooks(c *gin.Context) {
	v, err := a.Store.ListWebhooks(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取 Webhook 失败")
		return
	}
	httpx.OK(c, 200, v)
}

// adminCreateWebhook generates the signing secret, returns it exactly once,
// stores only the AES-GCM ciphertext.
func (a *App) adminCreateWebhook(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name         string   `json:"name"`
		URL          string   `json:"url"`
		Events       []string `json:"events"`
		AllowPrivate bool     `json:"allow_private"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := security.ValidateOutboundURL(strings.TrimSpace(in.URL), in.AllowPrivate); err != nil {
		httpx.Fail(c, 400, "WEBHOOK_URL_INVALID", err.Error())
		return
	}
	secret, err := security.RandomToken(32)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "生成签名密钥失败")
		return
	}
	enc, err := security.Encrypt(a.Cfg.MasterKey, secret)
	if err != nil {
		httpx.Fail(c, 500, "MASTER_KEY_INVALID", "MASTER_KEY_BASE64 未正确配置，无法安全保存 Webhook 密钥")
		return
	}
	v, err := a.Store.CreateWebhook(c, strings.TrimSpace(in.Name), strings.TrimSpace(in.URL), enc, sanitizeWebhookEvents(in.Events))
	if err != nil {
		httpx.Fail(c, 400, "WEBHOOK_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "webhook.create", "webhook", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"url": v.URL, "events": v.Events})
	httpx.OK(c, 201, map[string]any{"webhook": v, "secret": secret, "warning": "Secret 仅显示本次，用于校验 X-ShitIDC-Signature"})
}

func (a *App) adminUpdateWebhook(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name         string   `json:"name"`
		URL          string   `json:"url"`
		Events       []string `json:"events"`
		Active       *bool    `json:"active"`
		Secret       string   `json:"secret"`
		AllowPrivate bool     `json:"allow_private"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := security.ValidateOutboundURL(strings.TrimSpace(in.URL), in.AllowPrivate); err != nil {
		httpx.Fail(c, 400, "WEBHOOK_URL_INVALID", err.Error())
		return
	}
	enc := ""
	if strings.TrimSpace(in.Secret) != "" {
		var err error
		enc, err = security.Encrypt(a.Cfg.MasterKey, strings.TrimSpace(in.Secret))
		if err != nil {
			httpx.Fail(c, 500, "MASTER_KEY_INVALID", "MASTER_KEY_BASE64 未正确配置")
			return
		}
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	v, err := a.Store.UpdateWebhook(c, c.Param("id"), strings.TrimSpace(in.Name), strings.TrimSpace(in.URL), enc, sanitizeWebhookEvents(in.Events), active)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "WEBHOOK_NOT_FOUND", "Webhook 不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "WEBHOOK_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "webhook.update", "webhook", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, v)
}

func (a *App) adminDeleteWebhook(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteWebhook(c, c.Param("id")); err != nil {
		httpx.Fail(c, 404, "WEBHOOK_NOT_FOUND", "Webhook 不存在")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "webhook.delete", "webhook", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminWebhookDeliveries(c *gin.Context) {
	v, err := a.Store.ListWebhookDeliveries(c, c.Param("id"), parseIntDefault(c.Query("limit"), 100))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取投递记录失败")
		return
	}
	httpx.OK(c, 200, v)
}

// ---- security console ----

func (a *App) adminLoginLogs(c *gin.Context) {
	v, err := a.Store.ListLoginLogs(c, strings.TrimSpace(c.Query("email")), parseIntDefault(c.Query("limit"), 100))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取登录日志失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminSetUserStatus(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Active bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	u, err := a.Store.GetUserByIDOrPublicID(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取用户失败")
		return
	}
	if u.ID == p.User.ID {
		httpx.Fail(c, 409, "SELF_DISABLE_FORBIDDEN", "不能禁用自己的账户")
		return
	}
	if err := a.Store.SetUserStatus(c, p.User.ID, u.ID, in.Active, c.GetString("request_id")); errors.Is(err, store.ErrInvalidState) {
		httpx.Fail(c, 409, "USER_STATE_UNCHANGED", "用户状态未变化")
		return
	} else if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "更新用户状态失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"uid": u.ID, "active": in.Active})
}

// adminUserProfile returns a user's profile with PII masked unless the caller
// holds pii.read.full (第四十九阶段: 数据脱敏 + 完整查看审计).
func (a *App) adminUserProfile(c *gin.Context) {
	p, _ := getPrincipal(c)
	u, err := a.Store.GetUserByIDOrPublicID(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取用户失败")
		return
	}
	profile, err := a.Store.GetProfile(c, u.ID)
	if err != nil {
		profile = model.UserProfile{}
	}
	full := p.Permissions["pii.read.full"]
	if !full {
		profile.Phone = security.MaskPhone(profile.Phone)
		profile.QQ = security.MaskPhone(profile.QQ)
		profile.Address = ""
	} else {
		_ = a.Store.Audit(c, p.User.ID, "pii.view.full", "user", u.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"fields": []string{"phone", "qq", "address"}})
	}
	httpx.OK(c, 200, map[string]any{"user": u, "profile": profile, "unmasked": full})
}
