package api

import (
	"context"
	"errors"
	"html"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 用户工单升级（对齐魔方 CBAP TicketPremium 插件）：
// 用户侧：工单元数据 / 部门 / 关联产品 / 催单 / 评分；
// 后台：ticket-premium 全套（列表 / 详情 / 代建 / 回复 / 接单 / 保存 /
// 关闭 / 处理完成 / 转内部工单 / 备注 / 日志 / 部门 / 状态 / 预设回复 /
// 其他设置 / 统计排名）。

func ticketIDList(raw string) []int64 {
	out := []int64{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if id, err := strconv.ParseInt(part, 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
}

func ticketStringList(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// ticketTimeParam 兼容秒级时间戳与日期串（前端日期选择器）。
func ticketTimeParam(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil && n > 0 {
		t := time.Unix(n, 0)
		return &t
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return &t
	}
	if t, err := time.Parse("2006-01-02", raw); err == nil {
		return &t
	}
	return nil
}

// ticketPremiumError 把 store 错误映射成 HTTP 响应。
func ticketPremiumError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "TICKET_NOT_FOUND", "工单不存在")
	case errors.Is(err, store.ErrInvalidState),
		errors.Is(err, store.ErrTicketTypeInUse),
		errors.Is(err, store.ErrTicketDeptInUse),
		errors.Is(err, store.ErrTicketStatusInUse),
		errors.Is(err, store.ErrTicketSystemStatus),
		errors.Is(err, store.ErrTicketRateLimited),
		errors.Is(err, store.ErrTicketAlreadyTaken),
		errors.Is(err, store.ErrTicketReceiveFirst),
		errors.Is(err, store.ErrTicketFollowOnly),
		errors.Is(err, store.ErrTicketNotFinished),
		errors.Is(err, store.ErrTicketAlreadyScored),
		errors.Is(err, store.ErrTicketScoreRange):
		httpx.Fail(c, 400, "TICKET_INVALID", err.Error())
	default:
		httpx.Fail(c, 500, "INTERNAL_ERROR", err.Error())
	}
}

// ---- 邮件通知（4 个内置模板：创建 / 用户回复 / 客服回复 / 关闭） ----

func ticketMailVars(d store.TicketPremiumDetail, content string) map[string]string {
	return map[string]string{
		"ticket_id": d.Number,
		"subject":   d.Subject,
		"title":     d.Subject,
		"content":   html.EscapeString(content),
		"username":  d.UserEmail,
	}
}

// ticketMailTo 异步给单个收件人发通知邮件（模板可后台覆盖）。
func (a *App) ticketMailTo(to, tplName, fallbackSubject, fallbackBody string, vars map[string]string) {
	to = strings.TrimSpace(to)
	if to == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if !a.mailConfigured(ctx) {
			return
		}
		subject, body := a.renderMail(tplName, fallbackSubject, fallbackBody, vars)
		if err := a.deliverMail(ctx, to, subject, body); err != nil {
			log.Printf("ticket mail to %s failed: %v", to, err)
		}
	}()
}

// ticketMailStaff 异步通知全部客服邮箱。
func (a *App) ticketMailStaff(tplName, fallbackSubject, fallbackBody string, vars map[string]string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if !a.mailConfigured(ctx) {
			return
		}
		rcpts, err := a.Store.TicketStaffEmails(ctx)
		if err != nil || len(rcpts) == 0 {
			return
		}
		subject, body := a.renderMail(tplName, fallbackSubject, fallbackBody, vars)
		for _, rcpt := range rcpts {
			mctx, mcancel := context.WithTimeout(context.Background(), 25*time.Second)
			if err := a.deliverMail(mctx, rcpt, subject, body); err != nil {
				log.Printf("ticket notify mail to %s failed: %v", rcpt, err)
			}
			mcancel()
		}
	}()
}

// ---- 用户侧 ----

// ticketPremiumMeta 工单页元数据：状态列表 + 用户列表工单通知。
func (a *App) ticketPremiumMeta(c *gin.Context) {
	statuses, err := a.Store.ListTicketStatuses(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单状态失败")
		return
	}
	cfg, err := a.Store.GetTicketConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单配置失败")
		return
	}
	httpx.OK(c, 200, gin.H{
		"statuses":           statuses,
		"notice_open":        cfg.TicketNoticeOpen == "1",
		"notice_description": cfg.TicketNoticeDescription,
	})
}

// ticketPremiumDepartments 用户可见的工单部门（含类型）。
func (a *App) ticketPremiumDepartments(c *gin.Context) {
	v, err := a.Store.ListTicketDepartments(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单部门失败")
		return
	}
	httpx.OK(c, 200, v)
}

// ticketPremiumHosts 当前用户的产品（关联产品选择器）。
func (a *App) ticketPremiumHosts(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.ServicesForUser(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取产品失败")
		return
	}
	httpx.OK(c, 200, v)
}

// urgeTicket 用户催单：写催单时间并通知客服（站内 + 邮件）。
func (a *App) urgeTicket(c *gin.Context) {
	p, _ := getPrincipal(c)
	d, err := a.Store.GetTicketPremium(c, c.Param("id"), p.User.ID)
	if err != nil {
		ticketPremiumError(c, err)
		return
	}
	if err := a.Store.UrgeTicket(c, c.Param("id"), p.User.ID); err != nil {
		ticketPremiumError(c, err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.urge", "ticket", d.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	staff, err := a.Store.ListTicketStaff(c)
	if err == nil {
		title := "工单催单"
		body := "用户 " + d.UserEmail + " 催单：" + d.Subject
		link := "/admin/tickets/" + d.PublicID
		for _, st := range staff {
			_ = a.Store.InsertNotification(c, st.ID, "ticket", title, body, link)
		}
	}
	a.ticketMailStaff("ticket_client_reply", "[#"+d.Number+"] 工单催单："+d.Subject,
		"<p>用户催单，请尽快处理工单："+html.EscapeString(d.Subject)+"</p>", ticketMailVars(d, "用户催单"))
	httpx.OK(c, 200, gin.H{"ok": true})
}

// scoreTicket 用户评分（满意度 / 服务态度 / 处理时效）。
func (a *App) scoreTicket(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Satisfaction   float64 `json:"satisfaction"`
		Attitude       float64 `json:"attitude"`
		ProcessingTime float64 `json:"processing_time"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	avg, err := a.Store.ScoreTicketPremium(c, c.Param("id"), p.User.ID, in.Satisfaction, in.Attitude, in.ProcessingTime)
	if err != nil {
		ticketPremiumError(c, err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.score", "ticket", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"avg": avg})
	httpx.OK(c, 200, gin.H{"avg_score": avg})
}

// ---- 后台：列表 / 详情 ----

func (a *App) adminTicketPremiumList(c *gin.Context) {
	f := store.TicketPremiumFilter{
		Keywords:         strings.TrimSpace(c.Query("keywords")),
		TypeIDs:          ticketIDList(c.Query("ticket_type_ids")),
		Statuses:         ticketStringList(c.Query("status")),
		LastReplyAdminID: int64(parseIntDefault(c.Query("last_reply_admin_id"), 0)),
		AdminID:          int64(parseIntDefault(c.Query("admin_id"), 0)),
		Start:            ticketTimeParam(c.Query("start_time")),
		End:              ticketTimeParam(c.Query("end_time")),
		Page:             parseIntDefault(c.Query("page"), 1),
		Limit:            parseIntDefault(c.Query("limit"), 20),
	}
	if cid := strings.TrimSpace(c.Query("client_id")); cid != "" {
		if uid, err := a.Store.ResolveUserByPublicID(c, cid); err == nil {
			f.ClientID = uid
		}
	}
	v, total, err := a.Store.ListTicketsPremium(c, f)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": v, "total": total})
}

func (a *App) adminTicketPremiumDetail(c *gin.Context) {
	d, err := a.Store.GetTicketPremium(c, c.Param("id"), 0)
	if err != nil {
		ticketPremiumError(c, err)
		return
	}
	httpx.OK(c, 200, d)
}

// adminTicketPremiumCreate 客服代用户建单。
func (a *App) adminTicketPremiumCreate(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ClientID     string   `json:"client_id"`
		DepartmentID int64    `json:"department_id"`
		TicketTypeID int64    `json:"ticket_type_id"`
		Title        string   `json:"title"`
		Priority     string   `json:"priority"`
		HostIDs      []int64  `json:"host_ids"`
		Content      string   `json:"content"`
		Attachment   []string `json:"attachment"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	uid, err := a.Store.ResolveUserByPublicID(c, in.ClientID)
	if err != nil {
		httpx.Fail(c, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	t, err := a.Store.CreateTicketPremium(c, store.TicketPremiumCreateInput{
		UserID:        uid,
		PostAdminID:   p.User.ID,
		DepartmentID:  in.DepartmentID,
		TypeID:        in.TicketTypeID,
		Title:         in.Title,
		Priority:      in.Priority,
		HostIDs:       in.HostIDs,
		Message:       in.Content,
		AttachmentIDs: in.Attachment,
	})
	if err != nil {
		ticketPremiumError(c, err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.admin_create", "ticket", t.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"to_user": in.ClientID})
	if d, err := a.Store.GetTicketPremium(c, t.PublicID, 0); err == nil {
		a.ticketMailTo(d.UserEmail, "ticket_client_create", "[#"+d.Number+"] 工单已创建："+d.Subject,
			"<p>您的工单已创建，客服会尽快处理："+html.EscapeString(d.Subject)+"</p>", ticketMailVars(d, in.Content))
	}
	httpx.OK(c, 201, t)
}

// adminTicketPremiumReply 客服回复（受接单 / 跟进人开关约束）。
func (a *App) adminTicketPremiumReply(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Body       string   `json:"body"`
		Attachment []string `json:"attachment"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	body := strings.TrimSpace(in.Body)
	if body == "" || len([]rune(body)) > ticketBodyMaxRunes {
		httpx.Fail(c, 400, "INVALID_TICKET", "回复内容为空或超过 5000 字")
		return
	}
	d, err := a.Store.GetTicketPremium(c, c.Param("id"), 0)
	if err != nil {
		ticketPremiumError(c, err)
		return
	}
	cfg, err := a.Store.GetTicketConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单配置失败")
		return
	}
	if cfg.TicketReceiveReply == "1" && d.AdminUID == 0 {
		ticketPremiumError(c, store.ErrTicketReceiveFirst)
		return
	}
	if cfg.TicketFollowReply == "1" && d.AdminUID != 0 && d.AdminUID != p.User.ID {
		ticketPremiumError(c, store.ErrTicketFollowOnly)
		return
	}
	msg, _, err := a.Store.ReplyTicketPremium(c, c.Param("id"), p.User.ID, true, body, in.Attachment)
	if err != nil {
		ticketPremiumError(c, err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.admin_reply", "ticket", d.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"to_user": d.UserEmail})
	a.ticketMailTo(d.UserEmail, "ticket_admin_reply", "[#"+d.Number+"] 工单新回复："+d.Subject,
		"<p>您的工单有新的客服回复，请登录用户中心查看。</p>", ticketMailVars(d, body))
	httpx.OK(c, 201, msg)
}

// adminTicketPremiumAccept 接单（领取工单）。
func (a *App) adminTicketPremiumAccept(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.AcceptTicketPremium(c, c.Param("id"), p.User.ID); err != nil {
		ticketPremiumError(c, err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.accept", "ticket", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminTicketPremiumSave 保存详情页类型 / 状态 / 关联产品。
func (a *App) adminTicketPremiumSave(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		TicketTypeID int64   `json:"ticket_type_id"`
		Status       string  `json:"status"`
		HostIDs      []int64 `json:"host_ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SaveTicketPremiumFields(c, c.Param("id"), p.User.ID, in.TicketTypeID, in.Status, in.HostIDs); err != nil {
		ticketPremiumError(c, err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.save", "ticket", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminTicketPremiumStatus 关闭 / 重新打开 / 标记状态。
func (a *App) adminTicketPremiumStatus(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SetTicketPremiumStatus(c, c.Param("id"), p.User.ID, in.Status); err != nil {
		ticketPremiumError(c, err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.admin_status", "ticket", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"status": in.Status})
	if in.Status == "closed" {
		if d, err := a.Store.GetTicketPremium(c, c.Param("id"), 0); err == nil {
			a.ticketMailTo(d.UserEmail, "ticket_client_close", "[#"+d.Number+"] 工单已关闭："+d.Subject,
				"<p>您的工单已关闭："+html.EscapeString(d.Subject)+"</p>", ticketMailVars(d, "工单已关闭"))
		}
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminTicketPremiumProcessed 处理完成（可选同时关闭；随后提示用户评分）。
func (a *App) adminTicketPremiumProcessed(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Close bool `json:"close"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.FinishTicketPremium(c, c.Param("id"), p.User.ID, in.Close); err != nil {
		ticketPremiumError(c, err)
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.processed", "ticket", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"close": in.Close})
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 后台：备注 / 回复编辑删除 / 日志 ----

func (a *App) adminTicketPremiumAddNote(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.AddTicketNote(c, c.Param("id"), p.User.ID, in.Content)
	if err != nil {
		ticketPremiumError(c, err)
		return
	}
	httpx.OK(c, 201, v)
}

func (a *App) adminTicketPremiumUpdateNote(c *gin.Context) {
	p, _ := getPrincipal(c)
	id := parseIntDefault(c.Param("id"), 0)
	var in struct {
		Content string `json:"content"`
	}
	if id == 0 || c.ShouldBindJSON(&in) != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.UpdateTicketNote(c, int64(id), p.User.ID, in.Content); err != nil {
		ticketPremiumError(c, err)
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminTicketPremiumDeleteNote(c *gin.Context) {
	p, _ := getPrincipal(c)
	id := parseIntDefault(c.Param("id"), 0)
	if id == 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.DeleteTicketNote(c, int64(id), p.User.ID); err != nil {
		ticketPremiumError(c, err)
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminTicketPremiumUpdateReply(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Body string `json:"body"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.UpdateTicketReply(c, c.Param("id"), p.User.ID, in.Body); err != nil {
		ticketPremiumError(c, err)
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminTicketPremiumDeleteReply(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteTicketReply(c, c.Param("id"), p.User.ID); err != nil {
		ticketPremiumError(c, err)
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminTicketPremiumLog(c *gin.Context) {
	v, total, err := a.Store.ListTicketLogs(c, c.Param("id"), parseIntDefault(c.Query("page"), 1), parseIntDefault(c.Query("limit"), 20))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单日志失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": v, "total": total})
}

// adminTicketPremiumTurnInternal 把用户工单转成内部工单。
func (a *App) adminTicketPremiumTurnInternal(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Title        string  `json:"title"`
		TicketTypeID int64   `json:"ticket_type_id"`
		Priority     string  `json:"priority"`
		ClientID     string  `json:"client_id"`
		HostIDs      []int64 `json:"host_ids"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	var clientID int64
	if strings.TrimSpace(in.ClientID) != "" {
		if uid, err := a.Store.ResolveUserByPublicID(c, in.ClientID); err == nil {
			clientID = uid
		}
	}
	id, number, err := a.Store.TurnTicketInternal(c, c.Param("id"), p.User.ID, in.Title, in.TicketTypeID, in.Priority, clientID, in.HostIDs)
	if err != nil {
		httpx.Fail(c, 400, "TICKET_INVALID", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.turn_internal", "ticket", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"internal_id": id})
	httpx.OK(c, 201, gin.H{"id": id, "ticket_num": number})
}

// ---- 后台：部门设置 ----

type ticketPremiumDepartmentBody struct {
	Name            string  `json:"name"`
	AdminID         []int64 `json:"admin_id"`
	DirectorAdminID int64   `json:"director_admin_id"`
	Type            []struct {
		ID              int64   `json:"id"`
		Name            string  `json:"name"`
		ProcessingLimit float64 `json:"processing_limit"`
	} `json:"type"`
}

func (in ticketPremiumDepartmentBody) toInput() store.TicketDepartmentInput {
	out := store.TicketDepartmentInput{
		Name:            strings.TrimSpace(in.Name),
		AdminIDs:        in.AdminID,
		DirectorAdminID: in.DirectorAdminID,
	}
	for _, ty := range in.Type {
		out.Types = append(out.Types, store.TicketDepartmentType{
			ID:              ty.ID,
			Name:            strings.TrimSpace(ty.Name),
			ProcessingLimit: ty.ProcessingLimit,
		})
	}
	return out
}

// ticketPremiumAudit 记录 ticket_premium 后台操作。
func (a *App) ticketPremiumAudit(c *gin.Context, actor, id int64, action string, after any) {
	_ = a.Store.Audit(c, actor, action, "ticket_premium", strconv.FormatInt(id, 10), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, after)
}

func (a *App) adminTicketPremiumDepartments(c *gin.Context) {
	v, err := a.Store.ListTicketDepartments(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单部门失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": v})
}

func (a *App) adminTicketPremiumDepartmentCreate(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in ticketPremiumDepartmentBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	id, err := a.Store.CreateTicketDepartment(c, in.toInput())
	if err != nil {
		httpx.Fail(c, 400, "TICKET_DEPARTMENT_FAILED", err.Error())
		return
	}
	a.ticketPremiumAudit(c, p.User.ID, id, "ticket_premium.department.create", nil)
	httpx.OK(c, 201, gin.H{"id": id, "msg": "保存成功"})
}

func (a *App) adminTicketPremiumDepartmentUpdate(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in ticketPremiumDepartmentBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.UpdateTicketDepartment(c, id, in.toInput()); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "TICKET_DEPARTMENT_NOT_FOUND", "部门不存在")
			return
		}
		httpx.Fail(c, 400, "TICKET_DEPARTMENT_FAILED", err.Error())
		return
	}
	a.ticketPremiumAudit(c, p.User.ID, id, "ticket_premium.department.update", nil)
	httpx.OK(c, 200, gin.H{"msg": "保存成功"})
}

func (a *App) adminTicketPremiumDepartmentDelete(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteTicketDepartment(c, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "TICKET_DEPARTMENT_NOT_FOUND", "部门不存在")
			return
		}
		httpx.Fail(c, 400, "TICKET_DEPARTMENT_FAILED", err.Error())
		return
	}
	a.ticketPremiumAudit(c, p.User.ID, id, "ticket_premium.department.delete", nil)
	httpx.OK(c, 200, gin.H{"msg": "删除成功"})
}

// ---- 后台：工单状态 ----

func (a *App) adminTicketPremiumStatuses(c *gin.Context) {
	v, err := a.Store.ListTicketStatuses(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单状态失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": v})
}

func (a *App) adminTicketPremiumStatusCreate(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name     string `json:"name"`
		Color    string `json:"color"`
		Finished any    `json:"finished"`
		Status   any    `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	finished := ticketInternalFlag(in.Finished) == "1" || ticketInternalFlag(in.Status) == "1"
	id, err := a.Store.CreateTicketStatus(c, in.Name, in.Color, finished)
	if err != nil {
		httpx.Fail(c, 400, "TICKET_STATUS_FAILED", err.Error())
		return
	}
	a.ticketPremiumAudit(c, p.User.ID, id, "ticket_premium.status.create", nil)
	httpx.OK(c, 201, gin.H{"id": id, "msg": "保存成功"})
}

func (a *App) adminTicketPremiumStatusUpdate(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in struct {
		Name     string `json:"name"`
		Color    string `json:"color"`
		Finished any    `json:"finished"`
		Status   any    `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	finished := ticketInternalFlag(in.Finished) == "1" || ticketInternalFlag(in.Status) == "1"
	if err := a.Store.UpdateTicketStatus(c, id, in.Name, in.Color, finished); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "TICKET_STATUS_NOT_FOUND", "状态不存在")
			return
		}
		httpx.Fail(c, 400, "TICKET_STATUS_FAILED", err.Error())
		return
	}
	a.ticketPremiumAudit(c, p.User.ID, id, "ticket_premium.status.update", nil)
	httpx.OK(c, 200, gin.H{"msg": "保存成功"})
}

func (a *App) adminTicketPremiumStatusDelete(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteTicketStatus(c, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "TICKET_STATUS_NOT_FOUND", "状态不存在")
			return
		}
		httpx.Fail(c, 400, "TICKET_STATUS_FAILED", err.Error())
		return
	}
	a.ticketPremiumAudit(c, p.User.ID, id, "ticket_premium.status.delete", nil)
	httpx.OK(c, 200, gin.H{"msg": "删除成功"})
}

// ---- 后台：预设回复 ----

func (a *App) adminTicketPremiumPrereplies(c *gin.Context) {
	v, err := a.Store.ListTicketPrereplies(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取预设回复失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": v})
}

func (a *App) adminTicketPremiumPrereplyCreate(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	id, err := a.Store.CreateTicketPrereply(c, in.Content)
	if err != nil {
		httpx.Fail(c, 400, "TICKET_PREREPLY_FAILED", err.Error())
		return
	}
	a.ticketPremiumAudit(c, p.User.ID, id, "ticket_premium.prereply.create", nil)
	httpx.OK(c, 201, gin.H{"id": id, "msg": "保存成功"})
}

func (a *App) adminTicketPremiumPrereplyUpdate(c *gin.Context) {
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
	if err := a.Store.UpdateTicketPrereply(c, id, in.Content); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "TICKET_PREREPLY_NOT_FOUND", "预设回复不存在")
			return
		}
		httpx.Fail(c, 400, "TICKET_PREREPLY_FAILED", err.Error())
		return
	}
	a.ticketPremiumAudit(c, p.User.ID, id, "ticket_premium.prereply.update", nil)
	httpx.OK(c, 200, gin.H{"msg": "保存成功"})
}

func (a *App) adminTicketPremiumPrereplyDelete(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteTicketPrereply(c, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "TICKET_PREREPLY_NOT_FOUND", "预设回复不存在")
			return
		}
		httpx.Fail(c, 400, "TICKET_PREREPLY_FAILED", err.Error())
		return
	}
	a.ticketPremiumAudit(c, p.User.ID, id, "ticket_premium.prereply.delete", nil)
	httpx.OK(c, 200, gin.H{"msg": "删除成功"})
}

// ---- 后台：其他设置 ----

func (a *App) adminTicketPremiumConfig(c *gin.Context) {
	v, err := a.Store.GetTicketConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取配置失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminTicketPremiumSaveConfig(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		RefreshTime             any `json:"refresh_time"`
		TicketReceiveReply      any `json:"ticket_receive_reply"`
		TicketFollowReply       any `json:"ticket_follow_reply"`
		TicketNoticeOpen        any `json:"ticket_notice_open"`
		TicketNoticeDescription any `json:"ticket_notice_description"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	cfg := store.TicketConfig{
		RefreshTime:             ticketInternalText(in.RefreshTime),
		TicketReceiveReply:      ticketInternalFlag(in.TicketReceiveReply),
		TicketFollowReply:       ticketInternalFlag(in.TicketFollowReply),
		TicketNoticeOpen:        ticketInternalFlag(in.TicketNoticeOpen),
		TicketNoticeDescription: ticketInternalText(in.TicketNoticeDescription),
	}
	if cfg.RefreshTime == "" {
		cfg.RefreshTime = "180"
	}
	if err := a.Store.SaveTicketConfig(c, cfg); err != nil {
		httpx.Fail(c, 400, "TICKET_CONFIG_FAILED", err.Error())
		return
	}
	a.ticketPremiumAudit(c, p.User.ID, 0, "ticket_premium.config.save", nil)
	httpx.OK(c, 200, gin.H{"msg": "保存成功"})
}

// ---- 后台：客服人员 ----

func (a *App) adminTicketPremiumStaff(c *gin.Context) {
	v, err := a.Store.ListTicketStaff(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取人员列表失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": v})
}

// ---- 后台：统计与排名 ----

func ticketPremiumStatsFilter(c *gin.Context) store.TicketStatsFilter {
	f := store.TicketStatsFilter{
		Scope:        strings.TrimSpace(c.Query("scope")),
		DepartmentID: int64(parseIntDefault(c.Query("department_id"), 0)),
		AdminID:      int64(parseIntDefault(c.Query("admin_id"), 0)),
		Start:        ticketTimeParam(c.Query("start_time")),
		End:          ticketTimeParam(c.Query("end_time")),
	}
	if f.Scope == "" {
		if f.AdminID > 0 {
			f.Scope = "admin"
		} else if f.DepartmentID > 0 {
			f.Scope = "department"
		}
	}
	return f
}

func (a *App) adminTicketPremiumStatistics(c *gin.Context) {
	v, err := a.Store.TicketPremiumStatistics(c, ticketPremiumStatsFilter(c))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取统计失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminTicketPremiumScoreRankBy(byDepartment bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, err := a.Store.TicketPremiumScoreRank(c, byDepartment, ticketPremiumStatsFilter(c))
		if err != nil {
			httpx.Fail(c, 500, "INTERNAL_ERROR", "读取排名失败")
			return
		}
		httpx.OK(c, 200, gin.H{"rank": v})
	}
}

func (a *App) adminTicketPremiumTimeRankBy(byDepartment bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		v, err := a.Store.TicketPremiumTimeRank(c, byDepartment, ticketPremiumStatsFilter(c))
		if err != nil {
			httpx.Fail(c, 500, "INTERNAL_ERROR", "读取排名失败")
			return
		}
		httpx.OK(c, 200, gin.H{"rank": v})
	}
}
