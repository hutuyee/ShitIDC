package api

import (
	"errors"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 按量 / 超量计费接口（魔方 overages_* 的 Go 实现）。

// serviceUsage 返回一个服务的用量、水位与计量配置，供用户中心展示。
func (a *App) serviceUsage(c *gin.Context) {
	p, _ := getPrincipal(c)
	owner, err := a.Store.GetServiceOwner(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "SERVICE_NOT_FOUND", "服务不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取服务失败")
		return
	}
	if owner != p.User.ID && !p.Permissions["service.manage"] {
		httpx.Fail(c, 403, "FORBIDDEN", "无权查看该服务的用量")
		return
	}
	plan, err := a.Store.LoadMeteredPlan(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "SERVICE_NOT_FOUND", "服务不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取计量配置失败")
		return
	}
	usage, err := a.Store.ServiceUsage(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取用量失败")
		return
	}
	quote := store.ComputeOverage(plan, usage.DiskMB, usage.BilledDiskMB, usage.BWG, usage.BilledBWG)
	httpx.OK(c, 200, map[string]any{
		"plan":            plan,
		"usage_disk_mb":   usage.DiskMB,
		"usage_bw_gb":     usage.BWG,
		"billed_disk_mb":  usage.BilledDiskMB,
		"billed_bw_gb":    usage.BilledBWG,
		"pending_overage": quote,
		"reported_at":     usage.ReportedAt,
	})
}

// reportServiceUsage 供上游 Provider 回报用量（需要 API Token 或管理员）。
func (a *App) reportServiceUsage(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		DiskMB int64 `json:"disk_mb"`
		BWGB   int64 `json:"bw_gb"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	owner, err := a.Store.GetServiceOwner(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "SERVICE_NOT_FOUND", "服务不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取服务失败")
		return
	}
	if owner != p.User.ID && !p.Permissions["service.manage"] {
		httpx.Fail(c, 403, "FORBIDDEN", "无权上报该服务的用量")
		return
	}
	if err := a.Store.ReportUsage(c, c.Param("id"), in.DiskMB, in.BWGB); err != nil {
		httpx.Fail(c, 400, "USAGE_REPORT_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "service.usage_report", "service", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminSettleOverage 手动结算一个服务的超量费用（后台按钮）。
func (a *App) adminSettleOverage(c *gin.Context) {
	p, _ := getPrincipal(c)
	res, err := a.Store.SettleOverage(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "SERVICE_NOT_FOUND", "服务不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "OVERAGE_SETTLE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "service.overage_settle", "service", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, res)
	httpx.OK(c, 200, res)
}

// adminListOverageCharges 查看超量账单明细，便于对账与客户申诉。
func (a *App) adminListOverageCharges(c *gin.Context) {
	items, err := a.Store.ListOverageCharges(c, c.Param("id"), parseIntDefault(c.Query("limit"), 100))
	if err != nil {
		httpx.Fail(c, 500, "OVERAGE_LIST_FAILED", "读取超量账单失败")
		return
	}
	httpx.OK(c, 200, items)
}
