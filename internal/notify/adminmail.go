package notify

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/mail"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 值邮件通知管理员（对齐魔方 CBAP EmailNoticeAdmin 插件）。
//
// 与通知中心共用事件总线：命中已启用规则的事件在业务提交后异步给选定员工
// 发信。发信函数由调用方注入（server = api.App.DeliverMailVia，worker =
// queue.Client.MailSendVia）；失败只记日志，绝不影响业务流程。

// MailerFunc 与 queue.Client.MailSendVia / api.App.DeliverMailVia 的签名一致。
type MailerFunc func(ctx context.Context, to, subject, body, provider string) error

// InstallAdminMail wires the admin mail notice subscriber to the bus.
// server 与 worker 都要调用（两边的业务都会发事件）。
func InstallAdminMail(bus *events.Bus, st *store.Store, mailer MailerFunc) {
	if bus == nil || st == nil || mailer == nil {
		return
	}
	bus.Subscribe(func(ctx context.Context, e events.Event) {
		rules, err := st.GetEmailNoticeConfig(ctx)
		if err != nil {
			log.Printf("notify: load email notice config: %v", err)
			return
		}
		var rule *store.EmailNoticeRule
		for i := range rules {
			if rules[i].Event == e.Name {
				rule = &rules[i]
				break
			}
		}
		if rule == nil || !rule.Enable || rule.EmailTemplate == "" || len(rule.Admins) == 0 {
			return
		}
		go sendAdminMail(st, mailer, e, *rule)
	})
}

// sendAdminMail renders the configured template and mails every selected
// staff account. Runs detached from the emitting request.
func sendAdminMail(st *store.Store, mailer MailerFunc, e events.Event, rule store.EmailNoticeRule) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tmpl, err := st.GetMailTemplate(ctx, rule.EmailTemplate)
	if err != nil {
		log.Printf("notify: admin mail template %q missing for %s: %v", rule.EmailTemplate, e.Name, err)
		return
	}
	vars := adminMailVars(ctx, st, e)
	subject := strings.TrimSpace(mail.RenderTemplate(tmpl.Subject, vars))
	if subject == "" {
		subject = "【通知】" + events.Label(e.Name)
	}
	body := mail.RenderTemplate(tmpl.Body, vars)
	recipients, err := st.MailNoticeAdminEmails(ctx, rule.Admins)
	if err != nil {
		log.Printf("notify: resolve admin mail recipients for %s: %v", e.Name, err)
		return
	}
	for _, to := range recipients {
		if err := mailer(ctx, to, subject, body, rule.EmailName); err != nil {
			log.Printf("notify: admin mail to %s for %s failed: %v", to, e.Name, err)
		}
	}
}

// adminMailVars flattens the event payload into template placeholders and
// adds a few friendly aliases (event label, localized time, user email).
func adminMailVars(ctx context.Context, st *store.Store, e events.Event) map[string]string {
	when := e.OccurredAt.Format("2006-01-02 15:04:05")
	vars := map[string]string{
		"event":       e.Name,
		"event_name":  events.Label(e.Name),
		"occurred_at": when,
		"time":        when,
	}
	for k, v := range e.Data {
		if s, ok := scalarString(v); ok {
			vars[k] = s
		}
	}
	if uid := extractUserID(e); uid > 0 {
		vars["uid"] = strconv.FormatInt(uid, 10)
		if u, err := st.GetUserByID(ctx, uid); err == nil && u.Email != "" {
			if _, exists := vars["user_email"]; !exists {
				vars["user_email"] = u.Email
			}
		}
	}
	return vars
}

func scalarString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		if x {
			return "true", true
		}
		return "false", true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10), true
		}
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case json.Number:
		return x.String(), true
	default:
		return "", false
	}
}
