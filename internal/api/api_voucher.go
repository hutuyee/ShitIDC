package api

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 代金券（对齐魔方 CBAP 插件 IdcsmartVoucher）。
//
// 后台：券的定义、启停、发放与领取 / 使用记录；前台：我的券、可领取列表、
// 领取与下单前预览。抵扣发生在 new / renew / upgrade 三条订单链路上，
// 复用站内优惠券的「下单即核销、取消不返还」口径。

// voucherForm 是后台新增 / 修改代金券的表单（与插件 create_voucher 字段一致）。
type voucherForm struct {
	Code           string     `json:"code"`
	PriceCents     int64      `json:"price_cents"`
	Type           string     `json:"type"`
	Num            int64      `json:"num"`
	StartAt        *time.Time `json:"start_at"`
	EndAt          *time.Time `json:"end_at"`
	Product        []string   `json:"product"`
	ProductNeed    []string   `json:"product_need"`
	MinAmountCents int64      `json:"min_amount_cents"`
	UserType       string     `json:"user_type"`
	Onetime        bool       `json:"onetime"`
	UpgradeUse     bool       `json:"upgrade_use"`
	RenewUse       bool       `json:"renew_use"`
	Cycle          []string   `json:"cycle"`
	Notes          string     `json:"notes"`
}

func (f voucherForm) input() store.VoucherInput {
	in := store.VoucherInput{
		Code:                 strings.TrimSpace(f.Code),
		PriceCents:           f.PriceCents,
		Type:                 strings.ToLower(strings.TrimSpace(f.Type)),
		Num:                  f.Num,
		EndAt:                f.EndAt,
		ProductPublicIDs:     f.Product,
		ProductNeedPublicIDs: f.ProductNeed,
		MinAmountCents:       f.MinAmountCents,
		UserType:             strings.ToLower(strings.TrimSpace(f.UserType)),
		Onetime:              f.Onetime,
		UpgradeUse:           f.UpgradeUse,
		RenewUse:             f.RenewUse,
		Cycle:                f.Cycle,
		Notes:                strings.TrimSpace(f.Notes),
	}
	if f.StartAt != nil {
		in.StartAt = *f.StartAt
	}
	return in
}

// ---- 后台：代金券定义 ----

func (a *App) adminListVouchers(c *gin.Context) {
	items, err := a.Store.ListVouchers(c, c.Query("code"), c.Query("status"))
	if err != nil {
		httpx.Fail(c, 500, "VOUCHERS_FAILED", "读取代金券失败")
		return
	}
	httpx.OK(c, 200, items)
}

func (a *App) adminGetVoucher(c *gin.Context) {
	v, err := a.Store.GetVoucher(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "VOUCHER_NOT_FOUND", "代金券不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "VOUCHER_FAILED", "读取代金券失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminCreateVoucher(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in voucherForm
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	form := in.input()
	if !store.ValidVoucherCode(form.Code) {
		httpx.Fail(c, 400, "VOUCHER_CODE_INVALID", "券码须为 8 位且同时包含大写字母、小写字母与数字")
		return
	}
	v, err := a.Store.CreateVoucher(c, form)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "商品不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "VOUCHER_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "voucher.create", "voucher", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"code": v.Code, "price_cents": v.PriceCents, "type": v.Type})
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdateVoucher(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in voucherForm
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.UpdateVoucher(c, c.Param("id"), in.input())
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "VOUCHER_NOT_FOUND", "代金券不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "VOUCHER_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "voucher.update", "voucher", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"price_cents": in.PriceCents, "num": in.Num})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminDeleteVoucher(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteVoucher(c, c.Param("id")); err != nil {
		httpx.Fail(c, 404, "VOUCHER_NOT_FOUND", "代金券不存在")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "voucher.delete", "voucher", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) setVoucherEnabled(c *gin.Context, enabled bool) {
	p, _ := getPrincipal(c)
	if err := a.Store.SetVoucherEnabled(c, c.Param("id"), enabled); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "VOUCHER_NOT_FOUND", "代金券不存在")
			return
		}
		httpx.Fail(c, 400, "VOUCHER_UPDATE_FAILED", err.Error())
		return
	}
	action := "voucher.disable"
	if enabled {
		action = "voucher.enable"
	}
	_ = a.Store.Audit(c, p.User.ID, action, "voucher", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminEnableVoucher(c *gin.Context)  { a.setVoucherEnabled(c, true) }
func (a *App) adminDisableVoucher(c *gin.Context) { a.setVoucherEnabled(c, false) }

// adminCheckVoucherCode 新增表单的券码重复校验。
func (a *App) adminCheckVoucherCode(c *gin.Context) {
	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写券码")
		return
	}
	exists, err := a.Store.CheckVoucherCode(c, code)
	if err != nil {
		httpx.Fail(c, 500, "VOUCHER_FAILED", "校验券码失败")
		return
	}
	httpx.OK(c, 200, gin.H{"exists": exists, "valid": store.ValidVoucherCode(code)})
}

// adminVoucherTimes 发放弹窗展示每个用户已发放的次数。
func (a *App) adminVoucherTimes(c *gin.Context) {
	items, err := a.Store.VoucherTimes(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "VOUCHER_FAILED", "读取发放次数失败")
		return
	}
	httpx.OK(c, 200, items)
}

// adminSendVoucher 发放代金券：client_id 为 "all" 或用户 public_id 数组。
func (a *App) adminSendVoucher(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ClientID json.RawMessage `json:"client_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || len(in.ClientID) == 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择发放对象")
		return
	}
	var all bool
	var ids []string
	var one string
	if err := json.Unmarshal(in.ClientID, &one); err == nil {
		one = strings.TrimSpace(one)
		switch {
		case strings.EqualFold(one, "all"):
			all = true
		case one != "":
			ids = []string{one}
		}
	} else if err := json.Unmarshal(in.ClientID, &ids); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "发放对象格式错误")
		return
	}
	if !all && len(ids) == 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择发放对象")
		return
	}
	granted, skipped, err := a.Store.GrantVoucher(c, c.Param("id"), ids, all)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "VOUCHER_NOT_FOUND", "代金券不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "VOUCHER_SEND_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "voucher.send", "voucher", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"granted": granted, "skipped": skipped, "all": all})
	httpx.OK(c, 200, gin.H{"granted": granted, "skipped": skipped})
}

// adminVoucherRecords 领取 / 使用记录（对应插件 POST /voucher/record）。
func (a *App) adminVoucherRecords(c *gin.Context) {
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	items, total, err := a.Store.ListVoucherRecords(c, strings.TrimSpace(c.Query("voucher_id")), c.Query("keywords"), c.Query("use"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "VOUCHER_FAILED", "读取使用记录失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items, "total": total, "page": page, "limit": limit})
}

func (a *App) adminDeleteVoucherRecord(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteVoucherGrant(c, c.Param("id")); err != nil {
		httpx.Fail(c, 404, "VOUCHER_RECORD_NOT_FOUND", "记录不存在")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "voucher.record.delete", "voucher_grant", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- 前台：领取与预览 ----

func (a *App) myVouchers(c *gin.Context) {
	p, _ := getPrincipal(c)
	items, err := a.Store.MyVouchers(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "VOUCHERS_FAILED", "读取我的代金券失败")
		return
	}
	httpx.OK(c, 200, items)
}

func (a *App) claimableVouchers(c *gin.Context) {
	p, _ := getPrincipal(c)
	items, err := a.Store.ListClaimableVouchers(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "VOUCHERS_FAILED", "读取可领取代金券失败")
		return
	}
	httpx.OK(c, 200, items)
}

func (a *App) claimVoucher(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.ClaimVoucher(c, p.User.ID, c.Param("id")); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "VOUCHER_NOT_FOUND", "代金券不存在")
			return
		}
		httpx.Fail(c, 400, "VOUCHER_CLAIM_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "voucher.claim", "voucher", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// previewVoucher 下单前预览代金券抵扣金额（不核销）。
func (a *App) previewVoucher(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Code         string `json:"code"`
		ProductID    string `json:"product_id"`
		BillingCycle string `json:"billing_cycle"`
		Quantity     int    `json:"quantity"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	if strings.TrimSpace(in.BillingCycle) == "" {
		in.BillingCycle = "monthly"
	}
	prod, err := a.Store.GetProductPrice(c, in.ProductID, in.BillingCycle)
	if err != nil {
		httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "产品或周期不存在")
		return
	}
	discount, err := a.Store.PreviewVoucher(c, p.User.ID, in.Code, in.ProductID, in.BillingCycle, prod.PriceCents*int64(in.Quantity))
	if err != nil {
		httpx.Fail(c, 400, "VOUCHER_INVALID", err.Error())
		return
	}
	httpx.OK(c, 200, gin.H{"discount_cents": discount})
}
