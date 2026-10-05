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

// 客户关怀（对齐魔方 CBAP ClientCare 插件）。
//
// 后台按 push_object 筛选条件（指定用户 / 注册时长 / 产品数量 / 上次登录 /
// 产品状态 / 购买与删除时间等）圈定收件人，创建一次性 / 每天 / 每周 / 每月
// 的推送任务；调度器到点把站内信写进用户收件箱（/client-care/mails），
// 邮件类任务同时按指定通道发信。短信自定义内容本站暂不投递（字段保留）。

type clientCareJobBody struct {
	Title         string          `json:"title"`
	Type          int             `json:"type"`
	Content       string          `json:"content"`
	Subject       string          `json:"subject"`
	EmailName     string          `json:"email_name"`
	SmsName       string          `json:"sms_name"`
	SmsTemplateID int64           `json:"sms_template_id"`
	PushStartTime string          `json:"push_start_time"`
	PushEndTime   string          `json:"push_end_time"`
	SendCycle     string          `json:"send_cycle"`
	WeekDay       int             `json:"week_day"`
	MonthDay      int             `json:"month_day"`
	Hour          int             `json:"hour"`
	Minute        int             `json:"minute"`
	RepeatSend    bool            `json:"repeat_send"`
	PushObject    json.RawMessage `json:"push_object"`
}

// parseClientCareDay 解析推送日期（2006-01-02 或 RFC3339）。
func parseClientCareDay(raw string) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, false
	}
	if t, err := time.ParseInLocation("2006-01-02", raw, time.Local); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t, true
	}
	return time.Time{}, false
}
func (in clientCareJobBody) toInput() (store.ClientCareJobInput, string) {
	out := store.ClientCareJobInput{
		Title:         strings.TrimSpace(in.Title),
		Type:          in.Type,
		Content:       in.Content,
		Subject:       strings.TrimSpace(in.Subject),
		EmailName:     strings.TrimSpace(in.EmailName),
		SmsName:       strings.TrimSpace(in.SmsName),
		SmsTemplateID: in.SmsTemplateID,
		SendCycle:     strings.TrimSpace(in.SendCycle),
		WeekDay:       in.WeekDay,
		MonthDay:      in.MonthDay,
		Hour:          in.Hour,
		Minute:        in.Minute,
		RepeatSend:    in.RepeatSend,
		PushObject:    in.PushObject,
	}
	switch {
	case out.Title == "":
		return out, "请填写通知标题"
	case out.Type != 1 && out.Type != 2:
		return out, "通知形式不合法"
	case strings.TrimSpace(out.Content) == "":
		return out, "请填写推送内容"
	case out.Type == 2 && out.Subject == "":
		return out, "邮件推送需要填写邮件标题"
	}
	switch out.SendCycle {
	case "onetime", "day", "week", "month":
	default:
		return out, "推送周期不合法"
	}
	start, ok := parseClientCareDay(in.PushStartTime)
	if !ok {
		return out, "请选择推送开始时间"
	}
	end, ok := parseClientCareDay(in.PushEndTime)
	if !ok {
		return out, "请选择推送结束时间"
	}
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.Local)
	end = time.Date(end.Year(), end.Month(), end.Day(), 23, 59, 59, 0, time.Local)
	if end.Before(start) {
		return out, "推送结束时间不能早于开始时间"
	}
	if out.SendCycle == "week" && (out.WeekDay < 0 || out.WeekDay > 6) {
		return out, "请选择每周推送的星期"
	}
	if out.SendCycle == "month" && (out.MonthDay < 1 || out.MonthDay > 31) {
		return out, "请选择每月推送的日期"
	}
	if out.Hour < 0 || out.Hour > 23 || out.Minute < 0 || out.Minute > 59 {
		return out, "推送时间点不合法"
	}
	out.PushStartTime = start
	out.PushEndTime = end
	return out, ""
}

// adminListClientCareJobs 推送任务列表；status 取 wait/exec/suspended/finished/expired。
func (a *App) adminListClientCareJobs(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 100)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	items, total, err := a.Store.ListClientCareJobs(c, c.Query("keyword"), strings.TrimSpace(c.Query("status")), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_CARE_FAILED", "读取推送任务失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items, "count": total, "page": page})
}

// adminCreateClientCareJob 新建推送任务（服务端计算首次执行时间）。
func (a *App) adminCreateClientCareJob(c *gin.Context) {
	var in clientCareJobBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	input, msg := in.toInput()
	if msg != "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", msg)
		return
	}
	job, err := a.Store.CreateClientCareJob(c, input)
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_CARE_CREATE_FAILED", "创建推送任务失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "client_care.create", "client_care", job.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"title": job.Title, "type": job.Type, "cycle": job.SendCycle})
	}
	httpx.OK(c, 201, job)
}

// adminSetClientCareJobStatus 启停任务（对齐插件「推送状态」）。
func (a *App) adminSetClientCareJobStatus(c *gin.Context) {
	var in struct {
		Enable bool `json:"enable"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	job, err := a.Store.SetClientCareJobStatus(c, c.Param("id"), in.Enable)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CLIENT_CARE_NOT_FOUND", "推送任务不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_CARE_STATUS_FAILED", "更新推送状态失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "client_care.status", "client_care", job.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"enable": in.Enable, "status": job.Status})
	}
	httpx.OK(c, 200, job)
}

// adminDeleteClientCareJob 删除任务（收件箱保留历史）。
func (a *App) adminDeleteClientCareJob(c *gin.Context) {
	err := a.Store.DeleteClientCareJob(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CLIENT_CARE_NOT_FOUND", "推送任务不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_CARE_DELETE_FAILED", "删除推送任务失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "client_care.delete", "client_care", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminClientCareRecipients 按筛选条件预览推送名单。
func (a *App) adminClientCareRecipients(c *gin.Context) {
	var in struct {
		PushObject json.RawMessage `json:"push_object"`
		Limit      int             `json:"limit"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	list, total, err := a.Store.ClientCareRecipients(c, in.PushObject, in.Limit)
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_CARE_RECIPIENTS_FAILED", "读取推送名单失败")
		return
	}
	httpx.OK(c, 200, gin.H{"count": total, "list": list})
}

// adminClientCareOptions 推送表单的数据源（产品 / 接口 / 邮件通道 / 邮件模板）。
func (a *App) adminClientCareOptions(c *gin.Context) {
	opts, err := a.Store.ClientCareOptions(c)
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_CARE_OPTIONS_FAILED", "读取推送选项失败")
		return
	}
	httpx.OK(c, 200, opts)
}

// adminClientCareUsers 搜索用户（「指定用户」条件用）。
func (a *App) adminClientCareUsers(c *gin.Context) {
	list, err := a.Store.ClientCareUserSearch(c, c.Query("keyword"), parseIntDefault(c.Query("limit"), 50))
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_CARE_USERS_FAILED", "搜索用户失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

// ---- 用户端 ----

// listMyClientCareMails 我的站内信列表。
func (a *App) listMyClientCareMails(c *gin.Context) {
	p, _ := getPrincipal(c)
	list, err := a.Store.ListUserClientCareMails(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_CARE_MAILS_FAILED", "读取消息失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

// getMyClientCareMail 站内信详情（含上一篇 / 下一篇）。
func (a *App) getMyClientCareMail(c *gin.Context) {
	p, _ := getPrincipal(c)
	mail, err := a.Store.GetUserClientCareMail(c, p.User.ID, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CLIENT_CARE_MAIL_NOT_FOUND", "消息不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_CARE_MAIL_FAILED", "读取消息失败")
		return
	}
	httpx.OK(c, 200, mail)
}

// readMyClientCareMail 标记站内信已读。
func (a *App) readMyClientCareMail(c *gin.Context) {
	p, _ := getPrincipal(c)
	err := a.Store.MarkClientCareMailRead(c, p.User.ID, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "CLIENT_CARE_MAIL_NOT_FOUND", "消息不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "CLIENT_CARE_READ_FAILED", "标记已读失败")
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}
