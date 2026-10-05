package store

import (
	"context"
	"errors"
	"strings"
)

// 值邮件通知管理员（对齐魔方 CBAP EmailNoticeAdmin 插件）。
//
// 每个核心事件一行配置：启用开关、邮件模板（mail_templates.name）、
// 通知人员（后台员工用户 uid 列表）与可选的发信通道
// （mail_providers.public_id；留空 = 跟随系统当前默认通道 / 内置 SMTP）。

const emailNoticeConfigKey = "email_notice_admin"

// EmailNoticeRule 是单个事件的通知规则，字段名沿用插件契约用词。
type EmailNoticeRule struct {
	Event         string  `json:"event"`
	Enable        bool    `json:"email_enable"`
	EmailName     string  `json:"email_name"`
	EmailTemplate string  `json:"email_template"`
	Admins        []int64 `json:"notify_personnel"`
}

// GetEmailNoticeConfig 读取全部规则；未配置时返回空列表。
func (s *Store) GetEmailNoticeConfig(ctx context.Context) ([]EmailNoticeRule, error) {
	var out []EmailNoticeRule
	err := s.settingGet(ctx, emailNoticeConfigKey, &out)
	if errors.Is(err, ErrNotFound) {
		return []EmailNoticeRule{}, nil
	}
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []EmailNoticeRule{}
	}
	return out, nil
}

// SaveEmailNoticeConfig 保存规则（清洗空白、去重通知人员）。
func (s *Store) SaveEmailNoticeConfig(ctx context.Context, rules []EmailNoticeRule) error {
	clean := make([]EmailNoticeRule, 0, len(rules))
	for _, r := range rules {
		r.Event = strings.TrimSpace(r.Event)
		if r.Event == "" {
			continue
		}
		r.EmailName = strings.TrimSpace(r.EmailName)
		r.EmailTemplate = strings.TrimSpace(r.EmailTemplate)
		seen := map[int64]bool{}
		admins := make([]int64, 0, len(r.Admins))
		for _, id := range r.Admins {
			if id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			admins = append(admins, id)
		}
		r.Admins = admins
		clean = append(clean, r)
	}
	return s.settingSave(ctx, emailNoticeConfigKey, clean)
}

// NoticeAdmin 是通知人员候选（持有非 customer 角色的后台账号）。
type NoticeAdmin struct {
	UID   int64    `json:"uid"`
	Email string   `json:"email"`
	Roles []string `json:"roles"`
}

// ListMailNoticeAdmins 返回可被选为通知人员的员工账号（带角色名用于展示）。
func (s *Store) ListMailNoticeAdmins(ctx context.Context) ([]NoticeAdmin, error) {
	rows, err := s.DB.Query(ctx, `SELECT u.id, u.email,
 COALESCE(array_agg(DISTINCT COALESCE(NULLIF(r.description, ''), r.name)), '{}')
FROM users u
JOIN user_roles ur ON ur.user_id = u.id
JOIN roles r ON r.id = ur.role_id AND r.name <> 'customer'
WHERE u.status = 'active' AND u.deleted_at IS NULL AND u.email <> ''
GROUP BY u.id, u.email
ORDER BY u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NoticeAdmin{}
	for rows.Next() {
		var v NoticeAdmin
		if err := rows.Scan(&v.UID, &v.Email, &v.Roles); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MailNoticeAdminEmails 把选定的通知人员 uid 解析成邮箱集合。
func (s *Store) MailNoticeAdminEmails(ctx context.Context, uids []int64) ([]string, error) {
	if len(uids) == 0 {
		return []string{}, nil
	}
	rows, err := s.DB.Query(ctx, `SELECT email FROM users
WHERE id = ANY($1) AND status = 'active' AND deleted_at IS NULL AND email <> ''
ORDER BY id`, uids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		out = append(out, email)
	}
	return out, rows.Err()
}
