package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 电子合同（对齐魔方 CBAP EContract 插件）。
//
// 后台：模板管理 / 合同审核（通过 / 驳回 / 作废）/ 邮寄登记 / 下载合同文件 /
// 基础设置。用户端：可申请订单列表 / 申请合同 / 签订（签名图）/ 我的合同 /
// 下载。插件对接的第三方电子签通道加密不可读，签订为站内流程；
// 合同文件为可打印 HTML（替代插件的 PDF 生成）。

// ---- 设置 ----

func (a *App) adminGetEContractSettings(c *gin.Context) {
	cfg, err := a.Store.GetEContractSettings(c)
	if err != nil {
		httpx.Fail(c, 500, "E_CONTRACT_FAILED", "读取合同设置失败")
		return
	}
	httpx.OK(c, 200, gin.H{"config": cfg, "vars": store.EContractVars()})
}

func (a *App) adminSaveEContractSettings(c *gin.Context) {
	var in store.EContractSettings
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.MyUnit) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写我方单位名")
		return
	}
	if err := a.Store.SaveEContractSettings(c, in); err != nil {
		httpx.Fail(c, 500, "E_CONTRACT_FAILED", "保存合同设置失败")
		return
	}
	a.eContractAudit(c, "settings.save", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 模板 ----

func (a *App) adminListEContractTemplates(c *gin.Context) {
	list, err := a.Store.ListEContractTemplates(c, c.Query("active") == "1")
	if err != nil {
		httpx.Fail(c, 500, "E_CONTRACT_FAILED", "读取合同模板失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list, "vars": store.EContractVars()})
}

func (a *App) adminCreateEContractTemplate(c *gin.Context) {
	var in store.EContractTemplateInput
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写模板名称")
		return
	}
	v, err := a.Store.CreateEContractTemplate(c, in)
	if err != nil {
		failEContract(c, err, "新增合同模板失败")
		return
	}
	a.eContractAudit(c, "template.create", gin.H{"name": in.Name})
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdateEContractTemplate(c *gin.Context) {
	var in store.EContractTemplateInput
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写模板名称")
		return
	}
	v, err := a.Store.UpdateEContractTemplate(c, c.Param("id"), in)
	if err != nil {
		failEContract(c, err, "修改合同模板失败")
		return
	}
	a.eContractAudit(c, "template.update", gin.H{"name": in.Name})
	httpx.OK(c, 200, v)
}

func (a *App) adminCopyEContractTemplate(c *gin.Context) {
	v, err := a.Store.CopyEContractTemplate(c, c.Param("id"))
	if err != nil {
		failEContract(c, err, "复制模板失败")
		return
	}
	a.eContractAudit(c, "template.copy", nil)
	httpx.OK(c, 201, v)
}

func (a *App) adminDeleteEContractTemplate(c *gin.Context) {
	if err := a.Store.DeleteEContractTemplate(c, c.Param("id")); err != nil {
		failEContract(c, err, "删除模板失败")
		return
	}
	a.eContractAudit(c, "template.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 合同（后台） ----

func (a *App) adminListEContracts(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 50)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	list, total, err := a.Store.ListEContracts(c, c.Query("keyword"), c.Query("status"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "E_CONTRACT_FAILED", "读取合同失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list, "count": total, "page": page})
}

func (a *App) adminGetEContract(c *gin.Context) {
	v, err := a.Store.GetEContract(c, c.Param("id"))
	if err != nil {
		failEContract(c, err, "读取合同失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminReviewEContract(c *gin.Context) {
	var in struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.ReviewEContract(c, c.Param("id"), in.Action, in.Reason)
	if err != nil {
		failEContract(c, err, "审核失败")
		return
	}
	a.eContractAudit(c, "review."+in.Action, gin.H{"reason": in.Reason})
	httpx.OK(c, 200, v)
}

func (a *App) adminMailEContract(c *gin.Context) {
	var in struct {
		CourierCompany string `json:"courier_company"`
		CourierNumber  string `json:"courier_number"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.MailEContract(c, c.Param("id"), in.CourierCompany, in.CourierNumber)
	if err != nil {
		failEContract(c, err, "邮寄登记失败")
		return
	}
	a.eContractAudit(c, "mail", gin.H{"courier_company": in.CourierCompany, "courier_number": in.CourierNumber})
	httpx.OK(c, 200, v)
}

// adminDownloadEContract 下载可打印 HTML 合同文件。
func (a *App) adminDownloadEContract(c *gin.Context) {
	a.downloadEContract(c)
}

// ---- 合同（用户端） ----

func (a *App) myEContractOrders(c *gin.Context) {
	p, _ := getPrincipal(c)
	list, err := a.Store.ListUserEContractOrders(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "E_CONTRACT_FAILED", "读取可申请订单失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

func (a *App) myEContracts(c *gin.Context) {
	p, _ := getPrincipal(c)
	list, err := a.Store.GetUserEContracts(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "E_CONTRACT_FAILED", "读取合同失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

func (a *App) myApplyEContract(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		OrderID string `json:"order_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.OrderID) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择要申请合同的订单")
		return
	}
	v, err := a.Store.ApplyEContract(c, p.User.ID, strings.TrimSpace(in.OrderID))
	switch {
	case errors.Is(err, store.ErrEContractDisabled):
		httpx.Fail(c, 403, "E_CONTRACT_DISABLED", "合同功能未开启")
		return
	case errors.Is(err, store.ErrEContractTooOld):
		httpx.Fail(c, 400, "E_CONTRACT_TOO_OLD", "该订单已超出可申请合同的时间范围")
		return
	case errors.Is(err, store.ErrEContractExists):
		httpx.Fail(c, 409, "E_CONTRACT_EXISTS", "该订单已有有效合同")
		return
	case errors.Is(err, store.ErrEContractNoTemplate):
		httpx.Fail(c, 400, "E_CONTRACT_NO_TEMPLATE", "没有可用的合同模板")
		return
	case err != nil:
		httpx.Fail(c, 500, "E_CONTRACT_FAILED", "申请合同失败")
		return
	}
	httpx.OK(c, 201, v)
}

func (a *App) mySignEContract(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		SignImage string `json:"sign_image"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.SignEContract(c, p.User.ID, c.Param("id"), in.SignImage)
	if err != nil {
		failEContract(c, err, "签订失败（请检查签名图片，仅待签订状态可签）")
		return
	}
	httpx.OK(c, 200, v)
}

// myDownloadEContract 用户下载合同文件。
func (a *App) myDownloadEContract(c *gin.Context) {
	a.downloadEContract(c)
}

// downloadEContract 输出可打印 HTML；已作废 / 已驳回的合同不给下载。
func (a *App) downloadEContract(c *gin.Context) {
	v, err := a.Store.GetEContract(c, c.Param("id"))
	if err != nil {
		failEContract(c, err, "读取合同失败")
		return
	}
	if v.Status == "cancel" || v.Status == "reject" {
		httpx.Fail(c, 400, "E_CONTRACT_NOT_AVAILABLE", "该合同不可下载")
		return
	}
	cfg, _ := a.Store.GetEContractSettings(c)
	// 用户端只能下载自己的合同；管理员（有 e_contract.manage）可下载全部
	if p, ok := getPrincipal(c); ok {
		if v.UserID != p.User.ID && !p.Permissions["e_contract.manage"] {
			httpx.Fail(c, 403, "FORBIDDEN", "无权访问该合同")
			return
		}
	}
	c.Header("Content-Disposition", `attachment; filename="contract-`+v.Number+`.html"`)
	c.Data(200, "text/html; charset=utf-8", []byte(v.EContractHTML(cfg.CompanyChop, cfg.Logo)))
}

func (a *App) eContractAudit(c *gin.Context, action string, detail map[string]any) {
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "e_contract."+action, "e_contract", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, detail)
	}
}

func failEContract(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "E_CONTRACT_NOT_FOUND", "数据不存在")
	case errors.Is(err, store.ErrInvalidState), errors.Is(err, store.ErrEContractExists), errors.Is(err, store.ErrEContractTooOld):
		httpx.Fail(c, 400, "E_CONTRACT_INVALID_STATE", "数据状态不允许该操作")
	default:
		httpx.Fail(c, 500, "E_CONTRACT_FAILED", message)
	}
}
