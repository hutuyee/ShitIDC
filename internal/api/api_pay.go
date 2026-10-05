package api

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/payment"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"

	// 独立包实现的网关通过空白导入注册到支付注册表。
	// 易支付 / 支付宝 / Stripe 与 payment 包同包，随上面的导入自动生效；
	// 微信支付 / PayPal / USDT 放在子包里，需要显式引入。
	_ "github.com/hutuyee/ShitIDC/internal/payment/paypal"
	_ "github.com/hutuyee/ShitIDC/internal/payment/usdt"
	_ "github.com/hutuyee/ShitIDC/internal/payment/wechatpay"
)

func (a *App) cancelOrder(c *gin.Context) {
	p, _ := getPrincipal(c)
	o, err := a.Store.CancelOrder(c, p.User.ID, c.Param("id"))
	if errors.Is(err, store.ErrInvalidState) {
		httpx.Fail(c, 409, "ORDER_NOT_CANCELLABLE", "仅未支付的订单可以取消")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "取消订单失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "order.cancel", "order", o.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, o)
	a.Bus.Emit(a.eventCtx(c), events.OrderCancelled, map[string]any{"order_id": o.PublicID, "user_id": p.User.PublicID})
	httpx.OK(c, 200, o)
}

// listPaymentMethods returns enabled online payment channels for checkout.
func (a *App) listPaymentMethods(c *gin.Context) {
	items, err := a.Store.ListEnabledPaymentMethods(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取支付方式失败")
		return
	}
	out := make([]map[string]any, 0, len(items))
	for _, v := range items {
		types, _ := v.Config["pay_types"].([]any)
		payTypes := make([]string, 0, len(types))
		for _, t := range types {
			if s, ok := t.(string); ok {
				payTypes = append(payTypes, s)
			}
		}
		if len(payTypes) == 0 {
			if strings.EqualFold(v.Method, "manual") {
				payTypes = []string{"manual"}
			} else {
				payTypes = []string{"alipay", "wxpay"}
			}
		}
		out = append(out, map[string]any{"id": v.PublicID, "name": v.Name, "method": v.Method, "pay_types": payTypes})
	}
	httpx.OK(c, 200, out)
}

// payOrderOnline creates a pending payment for an unpaid order and returns
// the gateway redirect URL, built through the PaymentProvider abstraction
// (第七阶段) rather than a concrete channel.
func (a *App) payOrderOnline(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ProviderID string `json:"provider_id"`
		PayType    string `json:"pay_type"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.ProviderID) == "" {
		httpx.Fail(c, 400, "PROVIDER_REQUIRED", "请选择支付方式")
		return
	}
	prepared, err := a.Store.PrepareOrderOnlinePayment(c, p.User.ID, c.Param("id"), in.ProviderID, strings.TrimSpace(in.PayType))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "ORDER_NOT_FOUND", "订单或支付方式不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 409, "ORDER_NOT_PAYABLE", "订单当前状态不可支付")
		return
	case err != nil:
		httpx.Fail(c, 400, "PAYMENT_PREPARE_FAILED", err.Error())
		return
	}
	if strings.EqualFold(prepared.Provider.Method, "manual") {
		// 线下支付（user_custom 对齐）：不跳转网关，把后台配置的收款说明
		// 原样返回给前端展示，等管理员在后台「确认收款」完结订单。
		_ = a.Store.Audit(c, p.User.ID, "order.pay_online", "order", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"provider": prepared.Provider.Name, "pay_type": "manual", "manual": true})
		httpx.OK(c, 200, map[string]any{"html": manualPayMessage(prepared.Provider.Config), "out_trade_no": prepared.OutTradeNo, "need_confirm": true})
		return
	}
	payURL, err := a.gatewayPayURL(c, prepared)
	if err != nil {
		httpx.Fail(c, 502, "PAYMENT_BUILD_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "order.pay_online", "order", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"provider": prepared.Provider.Name, "pay_type": in.PayType})
	httpx.OK(c, 200, map[string]any{"pay_url": payURL, "out_trade_no": prepared.OutTradeNo})
}

// rechargeWallet creates a wallet top-up payment and returns the gateway URL.
func (a *App) rechargeWallet(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		AmountCents int64  `json:"amount_cents"`
		ProviderID  string `json:"provider_id"`
		PayType     string `json:"pay_type"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.ProviderID) == "" {
		httpx.Fail(c, 400, "PROVIDER_REQUIRED", "请选择支付方式")
		return
	}
	prepared, err := a.Store.PrepareRecharge(c, p.User.ID, in.AmountCents, in.ProviderID, strings.TrimSpace(in.PayType))
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "PROVIDER_NOT_FOUND", "支付方式不存在")
		return
	case err != nil:
		httpx.Fail(c, 400, "RECHARGE_FAILED", err.Error())
		return
	}
	payURL, err := a.gatewayPayURL(c, prepared)
	if err != nil {
		httpx.Fail(c, 502, "PAYMENT_BUILD_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "recharge.create", "wallet", prepared.OutTradeNo, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"amount_cents": in.AmountCents, "provider": prepared.Provider.Name})
	httpx.OK(c, 200, map[string]any{"pay_url": payURL, "out_trade_no": prepared.OutTradeNo})
}

// manualPayMessage 取线下支付渠道的收款说明（HTML）；与魔方 user_custom
// 插件把 seller_id 字段 htmlspecialchars_decode 后原样展示的口径一致。
func manualPayMessage(cfg map[string]any) string {
	if s, ok := cfg["message"].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

// gatewayPayURL decrypts the provider secret and asks the registered gateway
// for a checkout URL. Notify/return endpoints are derived from the method so
// each channel owns its callback path.
func (a *App) gatewayPayURL(c *gin.Context, prepared store.PreparedPayment) (string, error) {
	gw, ok := payment.Get(prepared.Provider.Method)
	if !ok {
		return "", errors.New("支付方式未实现: " + prepared.Provider.Method)
	}
	if len(a.Cfg.MasterKey) == 0 {
		return "", errors.New("服务器未配置 MASTER_KEY_BASE64，无法解密支付密钥")
	}
	secret, err := security.Decrypt(a.Cfg.MasterKey, prepared.Secret)
	if err != nil {
		return "", errors.New("支付密钥解密失败")
	}
	base := strings.TrimRight(a.Cfg.PublicBaseURL, "/")
	cfg := payment.ProviderConfig{Method: prepared.Provider.Method, GatewayURL: prepared.Provider.GatewayURL, MerchantID: prepared.Provider.MerchantID, Secret: secret}
	return gw.PayURL(c, cfg, payment.Prepared{
		OutTradeNo:  prepared.OutTradeNo,
		Subject:     prepared.Subject,
		AmountCents: prepared.AmountCents,
		Currency:    prepared.Currency,
		PayType:     prepared.PayType,
		NotifyURL:   base + "/api/v1/pay/notify/" + gw.Method(),
		ReturnURL:   base + "/api/v1/pay/return/" + gw.Method(),
		SiteName:    "ShitIDC",
	})
}

// notifyInput snapshots the callback request: raw body for exact-byte
// signatures (Stripe) plus merged query+form for form-based gateways.
func notifyInput(c *gin.Context) payment.NotifyInput {
	var raw []byte
	if c.Request.Body != nil {
		raw, _ = io.ReadAll(io.LimitReader(c.Request.Body, 1<<20))
	}
	query := url.Values{}
	for k, vs := range c.Request.URL.Query() {
		query[k] = append([]string(nil), vs...)
	}
	header := http.Header{}
	for k, vs := range c.Request.Header {
		header[k] = append([]string(nil), vs...)
	}
	input := payment.NotifyInput{Query: query, RawBody: raw, Header: header, Method: c.Request.Method}
	if c.Request.Method == http.MethodPost && strings.Contains(c.GetHeader("Content-Type"), "application/x-www-form-urlencoded") {
		if form, err := url.ParseQuery(string(raw)); err == nil {
			input.PostForm = form
		}
	}
	return input
}

// paymentProviderConfig loads and decrypts one gateway credential set.
func (a *App) paymentProviderConfig(ctx context.Context, publicID string) (payment.ProviderConfig, error) {
	pv, secret, err := a.Store.GetPaymentProviderCredentials(ctx, publicID)
	if err != nil {
		return payment.ProviderConfig{}, err
	}
	if len(a.Cfg.MasterKey) == 0 {
		return payment.ProviderConfig{}, errors.New("服务器未配置 MASTER_KEY_BASE64")
	}
	plain, err := security.Decrypt(a.Cfg.MasterKey, secret)
	if err != nil {
		return payment.ProviderConfig{}, errors.New("支付密钥解密失败")
	}
	return payment.ProviderConfig{Method: pv.Method, GatewayURL: pv.GatewayURL, MerchantID: pv.MerchantID, Secret: plain}, nil
}

// payNotify is the async server callback, dispatched by method through the
// gateway registry. It verifies signature + amount, then completes the order
// or recharge idempotently.
func (a *App) payNotify(c *gin.Context) {
	method := strings.ToLower(c.Param("method"))
	if method == "" {
		method = "epay"
	}
	gw, ok := payment.Get(method)
	if !ok {
		c.String(http.StatusBadRequest, "fail")
		return
	}
	input := notifyInput(c)
	values := input.Values()
	outTradeNo := strings.TrimSpace(values.Get("out_trade_no"))
	if outTradeNo == "" {
		log.Printf("%s notify rejected: missing out_trade_no", method)
		c.String(http.StatusBadRequest, "fail")
		return
	}
	pc, err := a.Store.GetPaymentContext(c, outTradeNo)
	if err != nil {
		log.Printf("%s notify rejected: payment %s not found: %v", method, outTradeNo, err)
		c.String(http.StatusBadRequest, "fail")
		return
	}
	cfg, err := a.paymentProviderConfig(c, pc.ProviderPublicID)
	if err != nil {
		log.Printf("%s notify rejected: provider config for %s: %v", method, outTradeNo, err)
		c.String(http.StatusBadRequest, "fail")
		return
	}
	if !strings.EqualFold(cfg.Method, method) {
		log.Printf("%s notify rejected: payment %s belongs to method %s", method, outTradeNo, cfg.Method)
		c.String(http.StatusBadRequest, "fail")
		return
	}
	res := gw.VerifyNotify(cfg, input)
	if !res.OK {
		log.Printf("%s notify rejected: %s: %v", method, outTradeNo, res.Err)
		c.String(http.StatusBadRequest, "fail")
		return
	}
	if res.AmountCents != pc.AmountCents {
		log.Printf("%s notify rejected: amount %d != expected %d for %s", method, res.AmountCents, pc.AmountCents, outTradeNo)
		c.String(http.StatusBadRequest, "fail")
		return
	}
	kind, userID, serviceIDs, renewServiceID, err := a.Store.CompleteOnlinePayment(c, outTradeNo, strings.TrimSpace(res.TradeNo), res.AmountCents)
	switch {
	case err == nil:
		a.afterPaymentCompleted(c, method, outTradeNo, res.AmountCents, kind, userID, serviceIDs, renewServiceID)
	case errors.Is(err, store.ErrAlreadyCompleted):
		// duplicate notify: acknowledge without re-processing (§15 Idempotency)
	case errors.Is(err, store.ErrAmountMismatch):
		_ = a.Store.SecurityEvent(c, userID, "payment.amount_mismatch", "warning", clientIP(c), map[string]any{"out_trade_no": outTradeNo})
		a.Bus.Emit(a.eventCtx(c), events.PaymentFailed, map[string]any{"out_trade_no": outTradeNo, "reason": "amount_mismatch"})
	default:
		log.Printf("%s notify failed: complete %s: %v", method, outTradeNo, err)
		c.String(http.StatusBadRequest, "fail")
		return
	}
	c.String(http.StatusOK, "success")
}

// afterPaymentCompleted 是支付完结后的公共收尾：审计、推广佣金、开通/续费
// 入队与事件广播。在线回调与后台「确认收款」共用，保证两条路径行为一致。
func (a *App) afterPaymentCompleted(c *gin.Context, method, outTradeNo string, amountCents int64, kind string, userID int64, serviceIDs []string, renewServiceID string) {
	_ = a.Store.Audit(c, userID, "payment.completed", "payment", outTradeNo, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"kind": kind, "amount_cents": amountCents, "method": method})
	if kind != "order" {
		a.Bus.Emit(a.eventCtx(c), events.WalletRecharged, map[string]any{"out_trade_no": outTradeNo, "uid": userID, "amount_cents": amountCents})
		return
	}
	// 推广系统: credit the referrer after a successful online payment.
	if settings, serr := a.Store.GetReferralSettings(c); serr == nil && settings.Enabled {
		if _, cerr := a.Store.PayReferralCommission(c, outTradeNo, settings.Percent); cerr != nil {
			log.Printf("referral commission failed for %s: %v", outTradeNo, cerr)
		}
	}
	if renewServiceID != "" && a.Queue != nil {
		_ = a.Queue.ServiceRenew(renewServiceID)
	}
	for _, id := range serviceIDs {
		if a.Queue != nil {
			_ = a.Queue.Provision(id)
		}
	}
	a.Bus.Emit(a.eventCtx(c), events.OrderPaid, map[string]any{"order_id": outTradeNo, "uid": userID, "method": method, "amount_cents": amountCents, "kind": kind})
	a.Bus.Emit(a.eventCtx(c), events.InvoicePaid, map[string]any{"order_id": outTradeNo, "uid": userID, "amount_cents": amountCents})
}

// payReturn is the browser redirect back from the gateway; the real state
// comes from the notify callback. We verify and bounce to the user center.
func (a *App) payReturn(c *gin.Context) {
	method := strings.ToLower(c.Param("method"))
	if method == "" {
		method = "epay"
	}
	target := strings.TrimRight(a.Cfg.PublicBaseURL, "/") + "/orders?pay=return"
	gw, ok := payment.Get(method)
	if ok {
		input := notifyInput(c)
		values := input.Values()
		if outTradeNo := strings.TrimSpace(values.Get("out_trade_no")); outTradeNo != "" {
			if pc, err := a.Store.GetPaymentContext(c, outTradeNo); err == nil {
				if cfg, err := a.paymentProviderConfig(c, pc.ProviderPublicID); err == nil && strings.EqualFold(cfg.Method, method) && gw.VerifyNotify(cfg, input).OK {
					if pc.Kind == "recharge" {
						target = strings.TrimRight(a.Cfg.PublicBaseURL, "/") + "/wallet?recharge=return"
					}
				}
			}
		}
	}
	c.Redirect(http.StatusFound, target)
}
