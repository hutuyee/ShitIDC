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

// 内部工单的「工单配置」「定时工单」「工单统计」（对齐 TicketInternalPremium）。

func ticketInternalFlag(v any) string {
	switch t := v.(type) {
	case bool:
		if t {
			return "1"
		}
	case float64:
		if t != 0 {
			return "1"
		}
	case string:
		if t == "1" || strings.EqualFold(t, "true") {
			return "1"
		}
	}
	return "0"
}

func ticketInternalText(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatInt(int64(t), 10)
	case bool:
		if t {
			return "1"
		}
	}
	return ""
}

// ---- 部门设置 ----

type ticketInternalDepartmentBody struct {
	Name            string  `json:"name"`
	AdminID         []int64 `json:"admin_id"`
	DirectorAdminID int64   `json:"director_admin_id"`
	Type            []struct {
		ID              int64  `json:"id"`
		Name            string `json:"name"`
		ProcessingLimit int    `json:"processing_limit"`
	} `json:"type"`
}

func (in ticketInternalDepartmentBody) toInput() store.TicketInternalDepartmentInput {
	out := store.TicketInternalDepartmentInput{
		Name:            strings.TrimSpace(in.Name),
		AdminIDs:        in.AdminID,
		DirectorAdminID: in.DirectorAdminID,
	}
	for _, ty := range in.Type {
		out.Types = append(out.Types, store.TicketInternalTypeInput{
			ID:              ty.ID,
			Name:            strings.TrimSpace(ty.Name),
			ProcessingLimit: ty.ProcessingLimit,
		})
	}
	return out
}

func (a *App) adminTicketInternalDepartments(c *gin.Context) {
	v, err := a.Store.ListTicketInternalDepartments(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单部门失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"list": v})
}

func (a *App) adminCreateTicketInternalDepartment(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in ticketInternalDepartmentBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	id, err := a.Store.SaveTicketInternalDepartment(c, 0, in.toInput())
	if err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_DEPARTMENT_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.department.create", nil)
	httpx.OK(c, 201, map[string]any{"id": id, "msg": "保存成功"})
}

func (a *App) adminUpdateTicketInternalDepartment(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in ticketInternalDepartmentBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if _, err := a.Store.SaveTicketInternalDepartment(c, id, in.toInput()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "TICKET_INTERNAL_DEPARTMENT_NOT_FOUND", "部门不存在")
			return
		}
		httpx.Fail(c, 400, "TICKET_INTERNAL_DEPARTMENT_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.department.update", nil)
	httpx.OK(c, 200, map[string]any{"msg": "保存成功"})
}

func (a *App) adminDeleteTicketInternalDepartment(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteTicketInternalDepartment(c, id); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_DEPARTMENT_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.department.delete", nil)
	httpx.OK(c, 200, map[string]any{"msg": "删除成功"})
}

// ---- 工单状态 ----

func (a *App) adminTicketInternalStatuses(c *gin.Context) {
	v, err := a.Store.ListTicketInternalStatuses(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单状态失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"list": v})
}

func (a *App) adminCreateTicketInternalStatus(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name   string `json:"name"`
		Color  string `json:"color"`
		Status any    `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	id, err := a.Store.CreateTicketInternalStatus(c, in.Name, in.Color, ticketInternalFlag(in.Status) == "1")
	if err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_STATUS_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.status.create", nil)
	httpx.OK(c, 201, map[string]any{"id": id, "msg": "保存成功"})
}

func (a *App) adminUpdateTicketInternalStatus(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in struct {
		Name   string `json:"name"`
		Color  string `json:"color"`
		Status any    `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.UpdateTicketInternalStatus(c, id, in.Name, in.Color, ticketInternalFlag(in.Status) == "1"); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_STATUS_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.status.update", nil)
	httpx.OK(c, 200, map[string]any{"msg": "保存成功"})
}

func (a *App) adminDeleteTicketInternalStatus(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteTicketInternalStatus(c, id); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_STATUS_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.status.delete", nil)
	httpx.OK(c, 200, map[string]any{"msg": "删除成功"})
}

// ---- 预设回复 ----

func (a *App) adminTicketInternalPrereplies(c *gin.Context) {
	v, err := a.Store.ListTicketInternalPrereplies(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取预设回复失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"list": v})
}

func (a *App) adminCreateTicketInternalPrereply(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	id, err := a.Store.CreateTicketInternalPrereply(c, in.Content)
	if err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_PREREPLY_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.prereply.create", nil)
	httpx.OK(c, 201, map[string]any{"id": id, "msg": "保存成功"})
}

func (a *App) adminUpdateTicketInternalPrereply(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.UpdateTicketInternalPrereply(c, id, in.Content); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_PREREPLY_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.prereply.update", nil)
	httpx.OK(c, 200, map[string]any{"msg": "保存成功"})
}

func (a *App) adminDeleteTicketInternalPrereply(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteTicketInternalPrereply(c, id); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_PREREPLY_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.prereply.delete", nil)
	httpx.OK(c, 200, map[string]any{"msg": "删除成功"})
}

// ---- 其他设置 ----

func (a *App) adminTicketInternalConfig(c *gin.Context) {
	v, err := a.Store.GetTicketInternalConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取配置失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminSaveTicketInternalConfig(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		OrderButton       any `json:"order_button"`
		FollowLimit       any `json:"follow_limit"`
		WillTimeoutNotice any `json:"will_timeout_notice"`
		RefreshTime       any `json:"refresh_time"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	cfg := store.TicketInternalConfig{
		OrderButton:       ticketInternalFlag(in.OrderButton),
		FollowLimit:       ticketInternalFlag(in.FollowLimit),
		WillTimeoutNotice: ticketInternalFlag(in.WillTimeoutNotice),
		RefreshTime:       ticketInternalText(in.RefreshTime),
	}
	if cfg.RefreshTime == "" {
		cfg.RefreshTime = "180"
	}
	if err := a.Store.SaveTicketInternalConfig(c, cfg); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存配置失败")
		return
	}
	a.ticketInternalAudit(c, p.User.ID, 0, "ticket_internal.config.update", nil)
	httpx.OK(c, 200, map[string]any{"msg": "保存成功"})
}

// ---- 定时工单 ----

type ticketInternalCronBody struct {
	Title        string `json:"title"`
	Content      string `json:"content"`
	CyclePeriod  int    `json:"cycle_period"`
	Unit         string `json:"unit"`
	StartTime    int64  `json:"start_time"`
	EndTime      int64  `json:"end_time"`
	TriggerTime  string `json:"trigger_time"`
	DepartmentID int64  `json:"department_id"`
	TypeID       int64  `json:"type_id"`
	AdminID      int64  `json:"admin_id"`
	Status       any    `json:"status"`
}

func (in ticketInternalCronBody) toInput(creator int64) store.TicketInternalCronInput {
	out := store.TicketInternalCronInput{
		Title:        in.Title,
		Content:      in.Content,
		CyclePeriod:  in.CyclePeriod,
		Unit:         in.Unit,
		TriggerTime:  in.TriggerTime,
		DepartmentID: in.DepartmentID,
		TypeID:       in.TypeID,
		AdminID:      in.AdminID,
		CreatorID:    creator,
		Status:       in.StatusInt(),
	}
	if in.StartTime > 0 {
		out.StartAt = time.Unix(in.StartTime, 0)
	}
	if in.EndTime > 0 {
		end := time.Unix(in.EndTime, 0)
		out.EndAt = &end
	}
	return out
}

// StatusInt 把表单里的开关 / 数字统一成 1 / 0。
func (in ticketInternalCronBody) StatusInt() int {
	if ticketInternalFlag(in.Status) == "1" {
		return 1
	}
	return 0
}

func (a *App) adminTicketInternalCron(c *gin.Context) {
	v, total, err := a.Store.ListTicketInternalCronJobs(c, parseIntDefault(c.Query("page"), 1), parseIntDefault(c.Query("limit"), 20))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取定时工单失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"list": v, "count": total})
}

func (a *App) adminCreateTicketInternalCron(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in ticketInternalCronBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	id, err := a.Store.CreateTicketInternalCronJob(c, in.toInput(p.User.ID))
	if err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_CRON_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.cron.create", nil)
	httpx.OK(c, 201, map[string]any{"id": id, "msg": "保存成功"})
}

func (a *App) adminGetTicketInternalCron(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	v, err := a.Store.GetTicketInternalCronJob(c, id)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "TICKET_INTERNAL_CRON_NOT_FOUND", "定时工单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取定时工单失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminUpdateTicketInternalCron(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in ticketInternalCronBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.UpdateTicketInternalCronJob(c, id, in.toInput(p.User.ID)); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_CRON_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.cron.update", nil)
	httpx.OK(c, 200, map[string]any{"msg": "保存成功"})
}

func (a *App) adminSetTicketInternalCronStatus(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in struct {
		Status any `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	status := 0
	if ticketInternalFlag(in.Status) == "1" {
		status = 1
	}
	if err := a.Store.SetTicketInternalCronStatus(c, id, status); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_CRON_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.cron.status", map[string]any{"status": status})
	httpx.OK(c, 200, map[string]any{"msg": "保存成功"})
}

func (a *App) adminDeleteTicketInternalCron(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteTicketInternalCronJob(c, id); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_CRON_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.cron.delete", nil)
	httpx.OK(c, 200, map[string]any{"msg": "删除成功"})
}

// ---- 统计与排名 ----

func (a *App) adminTicketInternalStatistics(c *gin.Context) {
	f := store.TicketInternalStatsFilter{
		Type:      strings.TrimSpace(c.Query("type")),
		ID:        int64(parseIntDefault(c.Query("id"), 0)),
		ScoreRole: strings.TrimSpace(c.Query("score_role")),
		Start:     ticketInternalTimeParam(c.Query("start_time")),
		End:       ticketInternalTimeParam(c.Query("end_time")),
	}
	v, err := a.Store.TicketInternalStatistics(c, f)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取统计失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminTicketInternalScoreRankBy(byDepartment bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, err := a.Store.TicketInternalScoreRank(c, byDepartment,
			int64(parseIntDefault(c.Query("department_id"), 0)), strings.TrimSpace(c.Query("score_role")),
			ticketInternalTimeParam(c.Query("start_time")), ticketInternalTimeParam(c.Query("end_time")))
		if err != nil {
			httpx.Fail(c, 500, "INTERNAL_ERROR", "读取排名失败")
			return
		}
		httpx.OK(c, 200, map[string]any{"rank": v})
	}
}

func (a *App) adminTicketInternalTimeRankBy(byDepartment bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, err := a.Store.TicketInternalTimeRank(c, byDepartment,
			int64(parseIntDefault(c.Query("department_id"), 0)),
			ticketInternalTimeParam(c.Query("start_time")), ticketInternalTimeParam(c.Query("end_time")))
		if err != nil {
			httpx.Fail(c, 500, "INTERNAL_ERROR", "读取排名失败")
			return
		}
		httpx.OK(c, 200, map[string]any{"rank": v})
	}
}
