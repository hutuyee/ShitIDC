package api

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 内部工单（对齐魔方 CBAP TicketInternalPremium 插件）。
// 后台「内部工单」页创建 / 接单 / 回复 / 转单 / 处理完成 / 关闭 / 评分，
// 详情含沟通记录、内部备注、预设回复与操作日志；配置、统计与定时工单在
// api_ticket_internal_settings.go。

func ticketInternalParseIDList(raw string) []int64 {
	out := []int64{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if v, err := strconv.ParseInt(part, 10, 64); err == nil && v > 0 {
			out = append(out, v)
		}
	}
	return out
}

func ticketInternalTimeParam(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return nil
	}
	t := time.Unix(v, 0)
	return &t
}

func ticketInternalPathID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "参数错误")
		return 0, false
	}
	return id, true
}

func (a *App) ticketInternalAudit(c *gin.Context, actor, id int64, action string, after any) {
	_ = a.Store.Audit(c, actor, action, "ticket_internal", strconv.FormatInt(id, 10), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, after)
}

// adminTicketInternalStaff 返回可指派的管理人员（部门 / 转单 / 筛选共用）。
func (a *App) adminTicketInternalStaff(c *gin.Context) {
	v, err := a.Store.ListTicketInternalStaff(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取人员列表失败")
		return
	}
	httpx.OK(c, 200, v)
}

// adminTicketInternalHosts 返回用户名下产品（关联产品选择器用）。
func (a *App) adminTicketInternalHosts(c *gin.Context) {
	uid, err := a.Store.ResolveUserByPublicID(c, c.Query("user_id"))
	if err != nil {
		httpx.Fail(c, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	v, err := a.Store.ServicesForUser(c, uid)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取产品失败")
		return
	}
	httpx.OK(c, 200, v)
}

// adminListTicketInternal 内部工单列表。
func (a *App) adminListTicketInternal(c *gin.Context) {
	p, _ := getPrincipal(c)
	f := store.TicketInternalFilter{
		Keywords:         strings.TrimSpace(c.Query("keywords")),
		TypeIDs:          ticketInternalParseIDList(c.Query("type_ids")),
		StatusIDs:        ticketInternalParseIDList(c.Query("status_ids")),
		CreatorID:        int64(parseIntDefault(c.Query("post_admin_id"), 0)),
		LastReplyAdminID: int64(parseIntDefault(c.Query("last_reply_admin_id"), 0)),
		AcceptorID:       int64(parseIntDefault(c.Query("order_admin_id"), 0)),
		Page:             parseIntDefault(c.Query("page"), 1),
		Limit:            parseIntDefault(c.Query("limit"), 20),
		ViewerID:         p.User.ID,
	}
	v, total, err := a.Store.ListTicketInternalTickets(c, f)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取内部工单失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"list": v, "count": total})
}

type ticketInternalCreateBody struct {
	Title        string          `json:"title"`
	DepartmentID int64           `json:"department_id"`
	TypeID       int64           `json:"type_id"`
	ClientID     string          `json:"client_id"`
	HostIDs      []int64         `json:"host_id"`
	SourceTicket string          `json:"ticket_id"`
	Priority     string          `json:"priority"`
	Content      string          `json:"content"`
	Notes        string          `json:"notes"`
	Attachment   json.RawMessage `json:"attachment"`
}

// adminCreateTicketInternal 新建内部工单。
func (a *App) adminCreateTicketInternal(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in ticketInternalCreateBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.Title) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写工单标题")
		return
	}
	if in.DepartmentID <= 0 || in.TypeID <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择工单部门与类型")
		return
	}
	clientID := int64(0)
	if strings.TrimSpace(in.ClientID) != "" {
		id, err := a.Store.ResolveUserByPublicID(c, in.ClientID)
		if err != nil {
			httpx.Fail(c, 400, "INVALID_REQUEST", "关联用户不存在")
			return
		}
		clientID = id
	}
	id, num, err := a.Store.CreateTicketInternal(c, store.TicketInternalCreateInput{
		Title:        in.Title,
		DepartmentID: in.DepartmentID,
		TypeID:       in.TypeID,
		ClientID:     clientID,
		HostIDs:      in.HostIDs,
		SourceTicket: in.SourceTicket,
		Priority:     in.Priority,
		Content:      in.Content,
		Notes:        in.Notes,
		Attachments:  in.Attachment,
		CreatorID:    p.User.ID,
	})
	if err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_CREATE_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.create", map[string]any{"ticket_num": num})
	httpx.OK(c, 201, map[string]any{"id": id, "ticket_num": num, "msg": "创建成功"})
}

// adminTicketInternalDetail 内部工单详情。
func (a *App) adminTicketInternalDetail(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	d, err := a.Store.GetTicketInternalTicket(c, id, p.User.ID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "TICKET_INTERNAL_NOT_FOUND", "工单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单失败")
		return
	}
	httpx.OK(c, 200, d)
}

type ticketInternalReplyBody struct {
	Content    string          `json:"content"`
	Attachment json.RawMessage `json:"attachment"`
}

// adminReplyTicketInternal 回复内部工单。
func (a *App) adminReplyTicketInternal(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in ticketInternalReplyBody
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	msg, err := a.Store.ReplyTicketInternal(c, id, p.User.ID, in.Content, in.Attachment)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "TICKET_INTERNAL_NOT_FOUND", "工单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_REPLY_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.reply", nil)
	httpx.OK(c, 201, msg)
}

// adminUpdateTicketInternalReply 编辑回复。
func (a *App) adminUpdateTicketInternalReply(c *gin.Context) {
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
	if err := a.Store.UpdateTicketInternalReply(c, id, p.User.ID, in.Content); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_REPLY_FAILED", err.Error())
		return
	}
	httpx.OK(c, 200, map[string]any{"msg": "保存成功"})
}

// adminDeleteTicketInternalReply 删除回复。
func (a *App) adminDeleteTicketInternalReply(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteTicketInternalReply(c, id); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_REPLY_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.reply.delete", nil)
	httpx.OK(c, 200, map[string]any{"msg": "删除成功"})
}

// adminAddTicketInternalNote 添加内部备注。
func (a *App) adminAddTicketInternalNote(c *gin.Context) {
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
	note, err := a.Store.AddTicketInternalNote(c, id, p.User.ID, in.Content)
	if err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_NOTE_FAILED", err.Error())
		return
	}
	httpx.OK(c, 201, note)
}

// adminUpdateTicketInternalNote 编辑内部备注。
func (a *App) adminUpdateTicketInternalNote(c *gin.Context) {
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
	if err := a.Store.UpdateTicketInternalNote(c, id, p.User.ID, in.Content); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_NOTE_FAILED", err.Error())
		return
	}
	httpx.OK(c, 200, map[string]any{"msg": "保存成功"})
}

// adminDeleteTicketInternalNote 删除内部备注。
func (a *App) adminDeleteTicketInternalNote(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteTicketInternalNote(c, id); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_NOTE_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.note.delete", nil)
	httpx.OK(c, 200, map[string]any{"msg": "删除成功"})
}

// adminTicketInternalLog 工单日志。
func (a *App) adminTicketInternalLog(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	v, err := a.Store.ListTicketInternalLogs(c, id)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取日志失败")
		return
	}
	httpx.OK(c, 200, map[string]any{"list": v})
}

// adminAcceptTicketInternal 接单。
func (a *App) adminAcceptTicketInternal(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.AcceptTicketInternal(c, id, p.User.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "TICKET_INTERNAL_NOT_FOUND", "工单不存在")
			return
		}
		httpx.Fail(c, 400, "TICKET_INTERNAL_ACCEPT_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.accept", nil)
	httpx.OK(c, 200, map[string]any{"msg": "接单成功"})
}

// adminForwardTicketInternal 转单。
func (a *App) adminForwardTicketInternal(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in struct {
		DepartmentID int64  `json:"department_id"`
		TypeID       int64  `json:"type_id"`
		AdminID      int64  `json:"admin_id"`
		Notes        string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if in.DepartmentID <= 0 || in.TypeID <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择转交部门与类型")
		return
	}
	if err := a.Store.ForwardTicketInternal(c, id, p.User.ID, in.DepartmentID, in.TypeID, in.AdminID, in.Notes); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_FORWARD_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.forward", map[string]any{"department_id": in.DepartmentID, "type_id": in.TypeID, "admin_id": in.AdminID})
	httpx.OK(c, 200, map[string]any{"msg": "转单成功"})
}

// adminUpdateTicketInternal 保存详情页修改（状态 / 部门类型 / 关联产品）。
func (a *App) adminUpdateTicketInternal(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in struct {
		StatusID *int64   `json:"status_id"`
		TypeID   *int64   `json:"type_id"`
		HostID   *[]int64 `json:"host_id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	statusID, typeID := int64(0), int64(0)
	if in.StatusID != nil {
		statusID = *in.StatusID
	}
	if in.TypeID != nil {
		typeID = *in.TypeID
	}
	var hosts []int64
	if in.HostID != nil {
		hosts = *in.HostID
		if hosts == nil {
			hosts = []int64{}
		}
	}
	if err := a.Store.UpdateTicketInternal(c, id, p.User.ID, statusID, typeID, hosts); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_UPDATE_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.update", nil)
	httpx.OK(c, 200, map[string]any{"msg": "保存成功"})
}

// adminCloseTicketInternal 关闭工单。
func (a *App) adminCloseTicketInternal(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	if err := a.Store.CloseTicketInternal(c, id, p.User.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "TICKET_INTERNAL_NOT_FOUND", "工单不存在")
			return
		}
		httpx.Fail(c, 400, "TICKET_INTERNAL_CLOSE_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.close", nil)
	httpx.OK(c, 200, map[string]any{"msg": "关闭成功"})
}

// adminFinishTicketInternal 处理完成（可选同时关闭）。
func (a *App) adminFinishTicketInternal(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
	p, _ := getPrincipal(c)
	var in struct {
		Close any `json:"close"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	closeIt := false
	switch v := in.Close.(type) {
	case bool:
		closeIt = v
	case float64:
		closeIt = v != 0
	case string:
		closeIt = v == "1" || strings.EqualFold(v, "true")
	}
	if err := a.Store.FinishTicketInternal(c, id, p.User.ID, closeIt); err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_FINISH_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.finish", map[string]any{"close": closeIt})
	httpx.OK(c, 200, map[string]any{"msg": "处理成功"})
}

// adminScoreTicketInternal 评分（发起人 / 主管）。
func (a *App) adminScoreTicketInternal(c *gin.Context) {
	id, ok := ticketInternalPathID(c)
	if !ok {
		return
	}
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
	role, err := a.Store.ScoreTicketInternal(c, id, p.User.ID, in.Satisfaction, in.Attitude, in.ProcessingTime)
	if err != nil {
		httpx.Fail(c, 400, "TICKET_INTERNAL_SCORE_FAILED", err.Error())
		return
	}
	a.ticketInternalAudit(c, p.User.ID, id, "ticket_internal.score", map[string]any{"role": role})
	httpx.OK(c, 200, map[string]any{"msg": "评分成功"})
}
