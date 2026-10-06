package api

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 业务经理（对齐魔方 CBAP IdcsmartSale 插件）。
//
// 销售成员 / 客户绑定 / 提成规则 / 销售设置 / 统计排名 / 提成详情。
// 插件的「任务奖励」与「充值提成」前端契约不足（服务端加密），不编造；
// 权限 sale.manage。

// ---- 销售成员 ----

type saleBody struct {
	AdminUID int64  `json:"admin_uid"`
	Name     string `json:"name"`
	Num      string `json:"num"`
	Email    string `json:"email"`
}

func (a *App) adminListSales(c *gin.Context) {
	list, err := a.Store.ListSales(c, c.Query("keyword"))
	if err != nil {
		httpx.Fail(c, 500, "SALES_FAILED", "读取销售成员失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

func (a *App) adminCreateSale(c *gin.Context) {
	var in saleBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" || in.AdminUID <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写销售姓名并选择后台账号")
		return
	}
	v, err := a.Store.CreateSale(c, store.SaleInput{AdminUID: in.AdminUID, Name: in.Name, Num: in.Num, Email: in.Email})
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 400, "SALE_ADMIN_NOT_FOUND", "后台账号不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "INVALID_REQUEST", "销售姓名与后台账号必填")
		return
	case err != nil:
		httpx.Fail(c, 500, "SALES_FAILED", "新增销售失败（该账号可能已是销售）")
		return
	}
	a.saleAudit(c, "sale.create", gin.H{"name": in.Name})
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdateSale(c *gin.Context) {
	var in saleBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写销售姓名")
		return
	}
	v, err := a.Store.UpdateSale(c, c.Param("id"), store.SaleInput{AdminUID: in.AdminUID, Name: in.Name, Num: in.Num, Email: in.Email})
	if err != nil {
		failSale(c, err, "修改销售失败")
		return
	}
	a.saleAudit(c, "sale.update", gin.H{"name": in.Name})
	httpx.OK(c, 200, v)
}

func (a *App) adminSetSaleStatus(c *gin.Context) {
	var in struct {
		Active *bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Active == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SetSaleActive(c, c.Param("id"), *in.Active); err != nil {
		failSale(c, err, "切换销售状态失败")
		return
	}
	a.saleAudit(c, "sale.status", gin.H{"active": *in.Active})
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminDeleteSale(c *gin.Context) {
	if err := a.Store.DeleteSale(c, c.Param("id")); err != nil {
		failSale(c, err, "删除销售失败")
		return
	}
	a.saleAudit(c, "sale.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 客户绑定 ----

func (a *App) adminListSaleClients(c *gin.Context) {
	list, err := a.Store.ListSaleClients(c, c.Query("sale_id"), c.Query("keyword"))
	if err != nil {
		httpx.Fail(c, 500, "SALE_CLIENTS_FAILED", "读取用户绑定失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

func (a *App) adminBindSaleClient(c *gin.Context) {
	var in struct {
		SaleID  string `json:"sale_id"`
		UserUID int64  `json:"user_uid"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.SaleID == "" || in.UserUID <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择销售与用户")
		return
	}
	if err := a.Store.BindSaleClient(c, in.SaleID, in.UserUID); err != nil {
		failSale(c, err, "绑定失败")
		return
	}
	a.saleAudit(c, "client.bind", gin.H{"sale": in.SaleID, "user_uid": in.UserUID})
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminUnbindSaleClient(c *gin.Context) {
	uid, _ := strconv.ParseInt(c.Param("uid"), 10, 64)
	if uid <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "用户 UID 无效")
		return
	}
	if err := a.Store.UnbindSaleClient(c, uid); err != nil {
		failSale(c, err, "解除绑定失败")
		return
	}
	a.saleAudit(c, "client.unbind", gin.H{"user_uid": uid})
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 提成规则 ----

type saleConfigBody struct {
	Scope           string  `json:"scope"`
	ProductID       string  `json:"product_id"`
	NewMode         string  `json:"new_mode"`
	NewValue        float64 `json:"new_value"`
	RenewMode       string  `json:"renew_mode"`
	RenewValue      float64 `json:"renew_value"`
	RepurchaseMode  string  `json:"repurchase_mode"`
	RepurchaseValue float64 `json:"repurchase_value"`
	UpgradeMode     string  `json:"upgrade_mode"`
	UpgradeValue    float64 `json:"upgrade_value"`
	Active          bool    `json:"active"`
}

// mode 字段里 fixed 以元提交、percent 以百分数提交；落库统一：
// fixed → 分（*100），percent → 基点（*100）。
func (a *App) adminListSaleConfigs(c *gin.Context) {
	list, err := a.Store.ListSaleCommissionConfigs(c)
	if err != nil {
		httpx.Fail(c, 500, "SALE_CONFIGS_FAILED", "读取提成规则失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

func (a *App) adminSaveSaleConfig(c *gin.Context) {
	var in saleConfigBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	conv := func(mode string, v float64) (string, int64) {
		if mode == "percent" {
			return "percent", int64(v * 100)
		}
		return "fixed", int64(v * 100)
	}
	newMode, newVal := conv(in.NewMode, in.NewValue)
	renewMode, renewVal := conv(in.RenewMode, in.RenewValue)
	repMode, repVal := conv(in.RepurchaseMode, in.RepurchaseValue)
	upMode, upVal := conv(in.UpgradeMode, in.UpgradeValue)
	v, err := a.Store.SaveSaleCommissionConfig(c, store.SaleCommissionConfigInput{
		Scope: in.Scope, ProductID: in.ProductID,
		NewMode: newMode, NewValue: newVal,
		RenewMode: renewMode, RenewValue: renewVal,
		RepurchaseMode: repMode, RepurchaseValue: repVal,
		UpgradeMode: upMode, UpgradeValue: upVal,
		Active: in.Active,
	})
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 400, "SALE_PRODUCT_NOT_FOUND", "商品不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "SALE_CONFIG_INVALID", "提成规则不合法（scope / 商品 / 比例范围）")
		return
	case err != nil:
		httpx.Fail(c, 500, "SALE_CONFIGS_FAILED", "保存提成规则失败")
		return
	}
	a.saleAudit(c, "config.save", gin.H{"scope": in.Scope, "product": in.ProductID})
	httpx.OK(c, 200, v)
}

func (a *App) adminDeleteSaleConfig(c *gin.Context) {
	if err := a.Store.DeleteSaleCommissionConfig(c, c.Param("id")); err != nil {
		failSale(c, err, "删除提成规则失败")
		return
	}
	a.saleAudit(c, "config.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 销售设置 ----

func (a *App) adminGetSaleSettings(c *gin.Context) {
	cfg, err := a.Store.GetSaleSettings(c)
	if err != nil {
		httpx.Fail(c, 500, "SALE_SETTINGS_FAILED", "读取销售设置失败")
		return
	}
	httpx.OK(c, 200, gin.H{
		"confirm_wait_day": cfg.ConfirmWaitDay,
		"big_min":          float64(cfg.BigMinCents) / 100,
		"big_max":          float64(cfg.BigMaxCents) / 100,
		"big_mode":         cfg.BigMode,
		"big_value":        saleValueOut(cfg.BigMode, cfg.BigValue),
	})
}

func (a *App) adminSaveSaleSettings(c *gin.Context) {
	var in struct {
		ConfirmWaitDay int     `json:"confirm_wait_day"`
		BigMin         float64 `json:"big_min"`
		BigMax         float64 `json:"big_max"`
		BigMode        string  `json:"big_mode"`
		BigValue       float64 `json:"big_value"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	var mode string
	var value int64
	if in.BigMode == "percent" {
		mode, value = "percent", int64(in.BigValue*100)
	} else {
		mode, value = "fixed", int64(in.BigValue*100)
	}
	if err := a.Store.SaveSaleSettings(c, store.SaleSettings{
		ConfirmWaitDay: in.ConfirmWaitDay,
		BigMinCents:    int64(in.BigMin * 100),
		BigMaxCents:    int64(in.BigMax * 100),
		BigMode:        mode,
		BigValue:       value,
	}); err != nil {
		httpx.Fail(c, 400, "SALE_SETTINGS_INVALID", "销售设置不合法")
		return
	}
	a.saleAudit(c, "settings.save", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

func saleValueOut(mode string, value int64) float64 {
	if mode == "percent" {
		return float64(value) / 100
	}
	return float64(value) / 100
}

// ---- 统计 / 排名 / 提成详情 ----

func saleRange(c *gin.Context) (string, string) {
	start := strings.TrimSpace(c.Query("start"))
	end := strings.TrimSpace(c.Query("end"))
	if t, err := time.Parse(time.RFC3339, start); err == nil {
		start = t.Format(time.RFC3339)
	} else {
		start = time.Now().AddDate(0, -1, 0).Format(time.RFC3339)
	}
	if t, err := time.Parse(time.RFC3339, end); err == nil {
		end = t.Format(time.RFC3339)
	} else {
		end = time.Now().Format(time.RFC3339)
	}
	return start, end
}

func (a *App) adminSaleStatistics(c *gin.Context) {
	start, end := saleRange(c)
	rows, err := a.Store.SaleStatistics(c, start, end)
	if err != nil {
		httpx.Fail(c, 500, "SALE_STATS_FAILED", "读取销售统计失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": rows, "start": start, "end": end})
}

func (a *App) adminSaleClientRanking(c *gin.Context) {
	start, end := saleRange(c)
	rows, err := a.Store.SaleClientRanking(c, start, end)
	if err != nil {
		httpx.Fail(c, 500, "SALE_STATS_FAILED", "读取消费排名失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": rows, "start": start, "end": end})
}

func (a *App) adminListSaleCommissions(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 100)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	start := strings.TrimSpace(c.Query("start"))
	end := strings.TrimSpace(c.Query("end"))
	list, total, err := a.Store.ListSaleCommissions(c, c.Query("sale_id"), c.Query("type"), c.Query("status"), start, end, limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "SALE_COMMISSIONS_FAILED", "读取提成记录失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list, "count": total, "page": page})
}

func (a *App) adminInvalidateSaleCommission(c *gin.Context) {
	if err := a.Store.InvalidateSaleCommission(c, c.Param("id")); err != nil {
		failSale(c, err, "置无效失败")
		return
	}
	a.saleAudit(c, "commission.invalid", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) saleAudit(c *gin.Context, action string, detail map[string]any) {
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "sale."+action, "sale", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, detail)
	}
}

func failSale(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "SALE_NOT_FOUND", "数据不存在")
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "SALE_INVALID_STATE", "数据状态不允许该操作")
	default:
		httpx.Fail(c, 500, "SALE_FAILED", message)
	}
}
