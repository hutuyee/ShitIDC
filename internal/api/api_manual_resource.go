package api

import (
	"errors"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 手动资源（对齐魔方 CBAP ManualResource 插件）。
//
// 供应商与资源台账 CRUD + 分配 / 空闲 + 电源操作（ipmi 模式经 internal/ipmi）。
// 插件的 VNC 控制台与重装 / 救援 / 破解密码依赖加密的 DCIM 客户端协议，
// 明确不支持；权限 manual_resource.manage。

type manualSupplierBody struct {
	Name    string `json:"name"`
	Contact string `json:"contact"`
	Notes   string `json:"notes"`
}

func (a *App) adminListManualSuppliers(c *gin.Context) {
	list, err := a.Store.ListManualSuppliers(c)
	if err != nil {
		httpx.Fail(c, 500, "MANUAL_SUPPLIERS_FAILED", "读取供应商失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

func (a *App) adminCreateManualSupplier(c *gin.Context) {
	var in manualSupplierBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写供应商名称")
		return
	}
	v, err := a.Store.CreateManualSupplier(c, store.ManualSupplierInput(in))
	if err != nil {
		httpx.Fail(c, 500, "MANUAL_SUPPLIERS_FAILED", "新增供应商失败")
		return
	}
	a.manualAudit(c, "supplier.create", gin.H{"name": in.Name})
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdateManualSupplier(c *gin.Context) {
	var in manualSupplierBody
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写供应商名称")
		return
	}
	v, err := a.Store.UpdateManualSupplier(c, c.Param("id"), store.ManualSupplierInput(in))
	if err != nil {
		failManual(c, err, "修改供应商失败")
		return
	}
	a.manualAudit(c, "supplier.update", gin.H{"name": in.Name})
	httpx.OK(c, 200, v)
}

func (a *App) adminDeleteManualSupplier(c *gin.Context) {
	if err := a.Store.DeleteManualSupplier(c, c.Param("id")); err != nil {
		failManual(c, err, "删除供应商失败")
		return
	}
	a.manualAudit(c, "supplier.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 资源 ----

type manualResourceBody struct {
	DedicatedIP     string  `json:"dedicated_ip"`
	AssignedIPs     string  `json:"assigned_ips"`
	Notes           string  `json:"notes"`
	Configuration   string  `json:"configuration"`
	Cost            float64 `json:"cost"`
	Username        string  `json:"username"`
	Password        string  `json:"password"`
	ControlMode     string  `json:"control_mode"`
	IpmiIP          string  `json:"ipmi_ip"`
	IpmiPort        int     `json:"ipmi_port"`
	IpmiVersion     string  `json:"ipmi_version"`
	DcimClientURL   string  `json:"dcim_client_url"`
	DcimClientID    string  `json:"dcim_client_id"`
	ControlUsername string  `json:"control_username"`
	ControlPassword string  `json:"control_password"`
	DueTime         string  `json:"due_time"`
	SupplierID      string  `json:"supplier_id"`
}

func manualResourceInput(in manualResourceBody) (store.ManualResourceInput, string) {
	var due *time.Time
	if t, err := time.Parse(time.RFC3339, in.DueTime); err == nil {
		due = &t
	}
	return store.ManualResourceInput{
		DedicatedIP:     in.DedicatedIP,
		AssignedIPs:     in.AssignedIPs,
		Notes:           in.Notes,
		Configuration:   in.Configuration,
		CostCents:       int64(in.Cost * 100),
		Username:        in.Username,
		Password:        in.Password,
		ControlMode:     in.ControlMode,
		IpmiIP:          in.IpmiIP,
		IpmiPort:        in.IpmiPort,
		IpmiVersion:     in.IpmiVersion,
		DcimClientURL:   in.DcimClientURL,
		DcimClientID:    in.DcimClientID,
		ControlUsername: in.ControlUsername,
		ControlPassword: in.ControlPassword,
		DueTime:         due,
		SupplierID:      in.SupplierID,
	}, ""
}

func (a *App) adminListManualResources(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 50)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	list, total, err := a.Store.ListManualResources(c, c.Query("keyword"), c.Query("supplier"), c.Query("status"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "MANUAL_RESOURCES_FAILED", "读取手动资源失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list, "count": total, "page": page})
}

func (a *App) adminGetManualResource(c *gin.Context) {
	v, err := a.Store.GetManualResource(c, c.Param("id"))
	if err != nil {
		failManual(c, err, "读取手动资源失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminCreateManualResource(c *gin.Context) {
	var in manualResourceBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.DedicatedIP) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写主 IP")
		return
	}
	input, _ := manualResourceInput(in)
	v, err := a.Store.CreateManualResource(c, input)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 400, "MANUAL_SUPPLIER_NOT_FOUND", "供应商不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "INVALID_REQUEST", "主 IP 与控制方式必填（ipmi / client）")
		return
	case err != nil:
		httpx.Fail(c, 500, "MANUAL_RESOURCES_FAILED", "新增手动资源失败")
		return
	}
	a.manualAudit(c, "resource.create", gin.H{"dedicated_ip": in.DedicatedIP})
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdateManualResource(c *gin.Context) {
	var in manualResourceBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.DedicatedIP) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写主 IP")
		return
	}
	input, _ := manualResourceInput(in)
	v, err := a.Store.UpdateManualResource(c, c.Param("id"), input)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "MANUAL_NOT_FOUND", "资源或供应商不存在")
		return
	case err != nil:
		httpx.Fail(c, 500, "MANUAL_RESOURCES_FAILED", "修改手动资源失败")
		return
	}
	a.manualAudit(c, "resource.update", gin.H{"dedicated_ip": in.DedicatedIP})
	httpx.OK(c, 200, v)
}

func (a *App) adminDeleteManualResource(c *gin.Context) {
	if err := a.Store.DeleteManualResource(c, c.Param("id")); err != nil {
		failManual(c, err, "删除手动资源失败")
		return
	}
	a.manualAudit(c, "resource.delete", nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminAssignManualResource 分配资源到某用户名下产品。
func (a *App) adminAssignManualResource(c *gin.Context) {
	var in struct {
		Service string `json:"service_id"`
		DueTime string `json:"due_time"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || strings.TrimSpace(in.Service) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择要分配到的产品（服务）")
		return
	}
	var due *time.Time
	if t, err := time.Parse(time.RFC3339, in.DueTime); err == nil {
		due = &t
	}
	v, err := a.Store.AssignManualResource(c, c.Param("id"), strings.TrimSpace(in.Service), due)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "MANUAL_NOT_FOUND", "资源不存在")
		return
	case errors.Is(err, store.ErrInvalidState):
		httpx.Fail(c, 400, "MANUAL_ASSIGN_INVALID", "产品不存在或已终止")
		return
	case err != nil:
		httpx.Fail(c, 500, "MANUAL_RESOURCES_FAILED", "分配失败")
		return
	}
	a.manualAudit(c, "resource.assign", gin.H{"service": in.Service})
	httpx.OK(c, 200, v)
}

// adminIdleManualResource 释放资源。
func (a *App) adminIdleManualResource(c *gin.Context) {
	v, err := a.Store.IdleManualResource(c, c.Param("id"))
	if err != nil {
		failManual(c, err, "空闲失败")
		return
	}
	a.manualAudit(c, "resource.idle", nil)
	httpx.OK(c, 200, v)
}

// adminManualPower 电源操作：status / on / off / reboot。
func (a *App) adminManualPower(c *gin.Context) {
	action := strings.TrimPrefix(c.Param("action"), "/")
	if action != "status" && action != "on" && action != "off" && action != "reboot" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "未知电源操作")
		return
	}
	v, power, err := a.Store.ManualPower(c, c.Param("id"), action)
	switch {
	case errors.Is(err, store.ErrManualUnsupported):
		httpx.Fail(c, 400, "MANUAL_UNSUPPORTED", "该资源的控制方式不支持电源操作（仅 IPMI 模式支持）")
		return
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "MANUAL_NOT_FOUND", "资源不存在")
		return
	case err != nil:
		httpx.Fail(c, 502, "MANUAL_POWER_FAILED", "电源操作失败："+err.Error())
		return
	}
	a.manualAudit(c, "resource.power."+action, gin.H{"power_on": power.PowerOn})
	httpx.OK(c, 200, gin.H{"resource": v, "power_on": power.PowerOn})
}

func (a *App) manualAudit(c *gin.Context, action string, detail map[string]any) {
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "manual."+action, "manual_resource", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, detail)
	}
}

func failManual(c *gin.Context, err error, message string) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "MANUAL_NOT_FOUND", "数据不存在")
	case errors.Is(err, store.ErrInvalidState), errors.Is(err, store.ErrManualUnsupported):
		httpx.Fail(c, 400, "MANUAL_INVALID_STATE", "数据状态不允许该操作")
	default:
		httpx.Fail(c, 500, "MANUAL_FAILED", message)
	}
}
