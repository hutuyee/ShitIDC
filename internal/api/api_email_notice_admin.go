package api

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 值邮件通知管理员（对齐魔方 CBAP EmailNoticeAdmin 插件）。
//
// 后台维护「动作 → 邮件接口 / 邮件模板 / 通知人员 / 启用」四列规则；
// 事件发生时由 internal/notify 按规则给选定员工发信（可指定发信通道）。

// adminGetEmailNoticeAdmin 返回动作清单（含已保存配置）+ 模板 / 通道 / 员工候选。
func (a *App) adminGetEmailNoticeAdmin(c *gin.Context) {
	rules, err := a.Store.GetEmailNoticeConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "EMAIL_NOTICE_FAILED", "读取邮件通知配置失败")
		return
	}
	templates, err := a.Store.ListMailTemplates(c)
	if err != nil {
		httpx.Fail(c, 500, "EMAIL_NOTICE_FAILED", "读取邮件模板失败")
		return
	}
	providers, err := a.Store.ListMailProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "EMAIL_NOTICE_FAILED", "读取邮件通道失败")
		return
	}
	admins, err := a.Store.ListMailNoticeAdmins(c)
	if err != nil {
		httpx.Fail(c, 500, "EMAIL_NOTICE_FAILED", "读取通知人员失败")
		return
	}
	saved := make(map[string]store.EmailNoticeRule, len(rules))
	for _, r := range rules {
		saved[r.Event] = r
	}
	list := make([]gin.H, 0, len(events.Catalog()))
	for _, info := range events.Catalog() {
		row := gin.H{
			"event":            info.Name,
			"name_lang":        info.Label,
			"email_name":       "",
			"email_template":   "",
			"notify_personnel": []int64{},
			"email_enable":     false,
		}
		if r, ok := saved[info.Name]; ok {
			row["email_name"] = r.EmailName
			row["email_template"] = r.EmailTemplate
			row["notify_personnel"] = r.Admins
			row["email_enable"] = r.Enable
		}
		list = append(list, row)
	}
	providerOut := make([]gin.H, 0, len(providers))
	for _, p := range providers {
		providerOut = append(providerOut, gin.H{
			"id": p.PublicID, "name": p.Name, "provider": p.Provider,
			"is_default": p.IsDefault, "active": p.Active,
		})
	}
	templateOut := make([]gin.H, 0, len(templates))
	for _, t := range templates {
		templateOut = append(templateOut, gin.H{"name": t.Name, "subject": t.Subject})
	}
	adminOut := make([]gin.H, 0, len(admins))
	for _, m := range admins {
		adminOut = append(adminOut, gin.H{"uid": m.UID, "email": m.Email, "roles": m.Roles})
	}
	httpx.OK(c, 200, gin.H{"list": list, "templates": templateOut, "providers": providerOut, "admins": adminOut})
}

// adminSaveEmailNoticeAdmin 保存全部规则：启用必须选模板与通知人员；
// 选了邮件接口也必须选模板（与插件前端校验一致，服务端再兜一层）。
func (a *App) adminSaveEmailNoticeAdmin(c *gin.Context) {
	var in struct {
		List []struct {
			Event           string  `json:"event"`
			EmailName       string  `json:"email_name"`
			EmailTemplate   string  `json:"email_template"`
			NotifyPersonnel []int64 `json:"notify_personnel"`
			EmailEnable     bool    `json:"email_enable"`
		} `json:"list"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	known := make(map[string]string, len(events.Catalog()))
	for _, info := range events.Catalog() {
		known[info.Name] = info.Label
	}
	providers, err := a.Store.ListMailProviders(c)
	if err != nil {
		httpx.Fail(c, 500, "EMAIL_NOTICE_FAILED", "读取邮件通道失败")
		return
	}
	providerSet := make(map[string]bool, len(providers))
	for _, p := range providers {
		providerSet[p.PublicID] = true
	}
	templates, err := a.Store.ListMailTemplates(c)
	if err != nil {
		httpx.Fail(c, 500, "EMAIL_NOTICE_FAILED", "读取邮件模板失败")
		return
	}
	tplSet := make(map[string]bool, len(templates))
	for _, t := range templates {
		tplSet[t.Name] = true
	}
	staff, err := a.Store.ListMailNoticeAdmins(c)
	if err != nil {
		httpx.Fail(c, 500, "EMAIL_NOTICE_FAILED", "读取通知人员失败")
		return
	}
	staffSet := make(map[int64]bool, len(staff))
	for _, m := range staff {
		staffSet[m.UID] = true
	}

	rules := make([]store.EmailNoticeRule, 0, len(in.List))
	seen := make(map[string]bool, len(in.List))
	for _, row := range in.List {
		event := strings.TrimSpace(row.Event)
		label, ok := known[event]
		if !ok {
			httpx.Fail(c, 400, "EMAIL_NOTICE_EVENT_UNKNOWN", "未知的邮件通知动作："+event)
			return
		}
		if seen[event] {
			httpx.Fail(c, 400, "EMAIL_NOTICE_DUPLICATE", "动作重复："+label)
			return
		}
		seen[event] = true
		rule := store.EmailNoticeRule{
			Event:         event,
			Enable:        row.EmailEnable,
			EmailName:     strings.TrimSpace(row.EmailName),
			EmailTemplate: strings.TrimSpace(row.EmailTemplate),
			Admins:        []int64{},
		}
		if rule.EmailName != "" && !providerSet[rule.EmailName] {
			httpx.Fail(c, 400, "EMAIL_NOTICE_PROVIDER_UNKNOWN", "邮件接口不存在："+rule.EmailName)
			return
		}
		for _, uid := range row.NotifyPersonnel {
			if !staffSet[uid] {
				httpx.Fail(c, 400, "EMAIL_NOTICE_ADMIN_UNKNOWN", "通知人员不存在或不是后台账号")
				return
			}
			rule.Admins = append(rule.Admins, uid)
		}
		if (rule.Enable || rule.EmailName != "") && rule.EmailTemplate == "" {
			httpx.Fail(c, 400, "EMAIL_NOTICE_TEMPLATE_REQUIRED", "「"+label+"」请选择邮件模板")
			return
		}
		if rule.EmailTemplate != "" && !tplSet[rule.EmailTemplate] {
			httpx.Fail(c, 400, "EMAIL_NOTICE_TEMPLATE_UNKNOWN", "邮件模板不存在："+rule.EmailTemplate)
			return
		}
		if rule.Enable && len(rule.Admins) == 0 {
			httpx.Fail(c, 400, "EMAIL_NOTICE_ADMINS_REQUIRED", "「"+label+"」启用前请选择通知人员")
			return
		}
		rules = append(rules, rule)
	}
	if err := a.Store.SaveEmailNoticeConfig(c, rules); err != nil {
		httpx.Fail(c, 500, "EMAIL_NOTICE_FAILED", "保存邮件通知配置失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "email_notice.save", "email_notice_admin", "", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"rules": len(rules)})
	}
	httpx.OK(c, 200, gin.H{"ok": true, "count": len(rules)})
}
