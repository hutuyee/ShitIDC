package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 后付费（对应魔方 pay_method = postpaid）。

// myCredit 返回当前用户的授信状况与占用。
func (a *App) myCredit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	acct, err := a.Store.CreditAccount(c, pr.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取授信信息失败")
		return
	}
	httpx.OK(c, 200, acct)
}

// createPostpaidOrder 下后付费订单。
//
// 与普通下单接口分开是刻意的：赊账是一个显式动作，不该藏在通用下单参数里
// 被前端误传。授权不足时返回 402，让前端引导用户去申请授信。
func (a *App) createPostpaidOrder(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		ProductID    string               `json:"product_id"`
		BillingCycle string               `json:"billing_cycle"`
		Quantity     int                  `json:"quantity"`
		CouponCode   string               `json:"coupon_code"`
		Currency     string               `json:"currency"`
		Config       []store.ConfigChoice `json:"config"`
		CustomFields map[string]string    `json:"custom_fields"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.ProductID) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择商品")
		return
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	if in.BillingCycle == "" {
		in.BillingCycle = "monthly"
	}
	o, err := a.Store.CreateOrderPostpaid(c, pr.User.ID, in.ProductID, in.BillingCycle, in.Quantity,
		strings.TrimSpace(in.CouponCode), store.OrderConfigInput{Choices: in.Config, CustomFields: in.CustomFields},
		strings.ToUpper(strings.TrimSpace(in.Currency)), true)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrPostpaidNotEnabled):
			httpx.Fail(c, 402, "POSTPAID_NOT_ENABLED", err.Error())
		case errors.Is(err, store.ErrCreditLimitExceeded):
			httpx.Fail(c, 402, "CREDIT_LIMIT_EXCEEDED", err.Error())
		case errors.Is(err, store.ErrNotFound):
			httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "商品不存在或已下架")
		default:
			httpx.Fail(c, 400, "ORDER_CREATE_FAILED", err.Error())
		}
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "order.create_postpaid", "order", o.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"total": o.TotalCents})
	httpx.OK(c, 201, o)
}

// ---- 管理端：授信管理 ----

// adminListCreditAccounts 列出所有已开通后付费的账号。
func (a *App) adminListCreditAccounts(c *gin.Context) {
	items, err := a.Store.ListCreditAccounts(c)
	if err != nil {
		httpx.Fail(c, 500, "CREDIT_LIST_FAILED", "读取授信列表失败")
		return
	}
	httpx.OK(c, 200, items)
}

// adminSetUserCredit 调整某个用户的授信。
func (a *App) adminSetUserCredit(c *gin.Context) {
	pr, _ := getPrincipal(c)
	var in struct {
		Enabled    bool   `json:"enabled"`
		LimitCents int64  `json:"limit_cents"`
		Days       int    `json:"credit_days"`
		Note       string `json:"note"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if in.Days <= 0 {
		in.Days = 30
	}
	if err := a.Store.SetCredit(c, c.Param("id"), in.Enabled, in.LimitCents, in.Days, in.Note, pr.User.ID); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			httpx.Fail(c, 404, "USER_NOT_FOUND", "用户不存在")
		default:
			// 「还有欠款不能停用」这类业务约束按 400 回，前端直接展示原因。
			httpx.Fail(c, 400, "CREDIT_UPDATE_FAILED", err.Error())
		}
		return
	}
	_ = a.Store.Audit(c, pr.User.ID, "credit.set", "user", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}
