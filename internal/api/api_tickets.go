package api

import (
	"context"
	"errors"
	"html"
	"log"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

const ticketBodyMaxRunes = 5000

// ---- user side ----

func (a *App) ticketDetail(c *gin.Context) {
	p, _ := getPrincipal(c)
	d, err := a.Store.GetTicketPremium(c, c.Param("id"), p.User.ID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "TICKET_NOT_FOUND", "工单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单失败")
		return
	}
	httpx.OK(c, 200, d)
}

// replyTicket appends the user's message (可带附件) and emails the staff mailboxes.
func (a *App) replyTicket(c *gin.Context) {
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
	ticket, err := a.Store.GetTicketPremium(c, c.Param("id"), p.User.ID)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "TICKET_NOT_FOUND", "工单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单失败")
		return
	}
	msg, _, err := a.Store.ReplyTicketPremium(c, c.Param("id"), p.User.ID, false, body, in.Attachment)
	if err != nil {
		httpx.Fail(c, 400, "TICKET_REPLY_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.reply", "ticket", ticket.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	a.Bus.Emit(a.eventCtx(c), events.TicketReplied, map[string]any{"ticket_id": ticket.PublicID, "user_id": p.User.PublicID, "by_staff": false})
	a.ticketMailStaff("ticket_client_reply", "[#"+ticket.Number+"] 工单新回复："+ticket.Subject,
		"<p>用户回复了工单："+html.EscapeString(ticket.Subject)+"</p>",
		map[string]string{"ticket_id": ticket.Number, "subject": ticket.Subject, "title": ticket.Subject, "content": html.EscapeString(msg.Body), "username": p.User.Email})
	httpx.OK(c, 201, msg)
}

// closeTicket lets the user close or reopen their own ticket.
func (a *App) closeTicket(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if in.Status == "" {
		in.Status = "closed"
	}
	status, err := a.Store.SetTicketStatus(c, c.Param("id"), p.User.ID, false, in.Status)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "TICKET_NOT_FOUND", "工单不存在")
		return
	}
	if errors.Is(err, store.ErrInvalidState) {
		httpx.Fail(c, 400, "TICKET_STATUS_INVALID", "状态不支持")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "更新工单状态失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.status", "ticket", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"status": status})
	httpx.OK(c, 200, map[string]string{"status": status})
}

// ---- staff side (permission ticket.manage, no full admin needed) ----

func (a *App) adminListTickets(c *gin.Context) {
	v, err := a.Store.ListAllTickets(c, strings.TrimSpace(c.Query("status")), parseIntDefault(c.Query("limit"), 200))
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminTicketDetail(c *gin.Context) {
	d, err := a.Store.GetTicket(c, c.Param("id"), 0)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "TICKET_NOT_FOUND", "工单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单失败")
		return
	}
	httpx.OK(c, 200, d)
}

// adminReplyTicket appends the staff reply and emails the ticket owner.
func (a *App) adminReplyTicket(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Body string `json:"body"`
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
	ticket, err := a.Store.GetTicket(c, c.Param("id"), 0)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "TICKET_NOT_FOUND", "工单不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取工单失败")
		return
	}
	msg, _, err := a.Store.ReplyTicket(c, c.Param("id"), p.User.ID, true, body)
	if err != nil {
		httpx.Fail(c, 400, "TICKET_REPLY_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.staff_reply", "ticket", ticket.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"to_user": ticket.UserEmail})
	a.Bus.Emit(a.eventCtx(c), events.TicketReplied, map[string]any{"ticket_id": ticket.PublicID, "user_id": p.User.PublicID, "by_staff": true})
	a.mailTicketNotification(c, ticket.Subject, ticket.PublicID, true, ticket.UserEmail, msg.Body)
	httpx.OK(c, 201, msg)
}

func (a *App) adminTicketStatus(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Status string `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	status, err := a.Store.SetTicketStatus(c, c.Param("id"), p.User.ID, true, in.Status)
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "TICKET_NOT_FOUND", "工单不存在")
		return
	}
	if errors.Is(err, store.ErrInvalidState) {
		httpx.Fail(c, 400, "TICKET_STATUS_INVALID", "状态仅支持 open / pending / closed")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "更新工单状态失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "ticket.staff_status", "ticket", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"status": status})
	httpx.OK(c, 200, map[string]string{"status": status})
}

// mailTicketNotification emails the other side of a ticket conversation via
// the mail.send queue (falls back to a detached goroutine when Redis is
// unavailable). The body is HTML-escaped before insertion so ticket content
// can never inject markup into mails.
func (a *App) mailTicketNotification(_ *gin.Context, subject, ticketID string, toUser bool, to string, body string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if strings.TrimSpace(to) == "" {
			return
		}
		if !a.mailConfigured(ctx) {
			return // 邮件服务未配置；站内会话仍然可用
		}
		var recipients []string
		if toUser {
			recipients = []string{to}
		} else {
			var err error
			recipients, err = a.Store.TicketStaffEmails(ctx)
			if err != nil || len(recipients) == 0 {
				return
			}
		}
		side := "用户"
		if toUser {
			side = "客服"
		}
		mailSubject := "[#" + shortTicketRef(ticketID) + "] 工单新回复：" + subject
		escaped := html.EscapeString(body)
		htmlBody := "<p>您的工单有新的" + side + "回复：</p><blockquote>" + strings.ReplaceAll(escaped, "\n", "<br/>") + "</blockquote><p>请登录用户中心在「工单」中查看并继续对话。</p>"
		for _, rcpt := range recipients {
			mctx, mcancel := context.WithTimeout(context.Background(), 25*time.Second)
			if err := a.deliverMail(mctx, rcpt, mailSubject, htmlBody); err != nil {
				log.Printf("ticket notify mail to %s failed: %v", rcpt, err)
			}
			mcancel()
		}
	}()
}

func shortTicketRef(publicID string) string {
	if len(publicID) > 8 {
		return publicID[:8]
	}
	return publicID
}
