package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// MailSettings is persisted in system_settings under the "mail" key. The SMTP
// password is stored AES-GCM encrypted (master key), never in plaintext.
type MailSettings struct {
	SMTPHost       string `json:"smtp_host"`
	SMTPPort       int    `json:"smtp_port"`
	SMTPUsername   string `json:"smtp_username"`
	SMTPPasswordEn string `json:"smtp_password_encrypted"`
	SMTPFrom       string `json:"smtp_from"`
	FromName       string `json:"from_name"`
	SMTPEncryption string `json:"smtp_encryption"`
	VerifyRequired bool   `json:"verify_required"`
}

const mailSettingsKey = "mail"

func (s *Store) GetMailSettings(ctx context.Context) (MailSettings, error) {
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT value FROM system_settings WHERE key=$1`, mailSettingsKey).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return MailSettings{}, ErrNotFound
	}
	if err != nil {
		return MailSettings{}, err
	}
	var out MailSettings
	err = json.Unmarshal(raw, &out)
	return out, err
}

func (s *Store) SaveMailSettings(ctx context.Context, settings MailSettings) error {
	b, err := json.Marshal(settings)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO system_settings(key,value,updated_at) VALUES($1,$2,now())
ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=now()`, mailSettingsKey, b)
	return err
}

// ListAuditFiltered returns audit/behavior logs with optional filters.
func (s *Store) ListAuditFiltered(ctx context.Context, action, actorRef string, limit int) ([]model.AuditEntry, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var actorID int64
	actorRef = strings.TrimSpace(actorRef)
	// actor 支持三种写法：纯数字 UID、UUID、邮箱。
	// 后台排查日志时手上有什么就用什么，不必先去用户列表换一次 ID。
	switch {
	case actorRef == "":
	case isAllDigits(actorRef):
		_ = s.DB.QueryRow(ctx, `SELECT id FROM users WHERE id=$1`, actorRef).Scan(&actorID)
	case strings.Contains(actorRef, "@"):
		_ = s.DB.QueryRow(ctx, `SELECT id FROM users WHERE lower(email)=lower($1)`, actorRef).Scan(&actorID)
	default:
		_ = s.DB.QueryRow(ctx, `SELECT id FROM users WHERE public_id::text=$1`, actorRef).Scan(&actorID)
	}
	rows, err := s.DB.Query(ctx, `SELECT a.id,coalesce(a.actor_user_id,0),coalesce(u.email,''),a.action,a.object_type,a.object_id,
coalesce(a.request_id,''),coalesce(a.ip::text,''),coalesce(a.user_agent,''),a.before_data,a.after_data,a.created_at
FROM audit_logs a LEFT JOIN users u ON u.id=a.actor_user_id
WHERE ($1='' OR a.action LIKE $1||'%') AND ($2=0 OR a.actor_user_id=$2)
ORDER BY a.id DESC LIMIT $3`, strings.TrimSpace(action), actorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AuditEntry{}
	for rows.Next() {
		var v model.AuditEntry
		var before, after []byte
		if err := rows.Scan(&v.ID, &v.ActorUID, &v.ActorMail, &v.Action, &v.Kind, &v.ObjID, &v.RequestID, &v.IP, &v.UserAgent, &before, &after, &v.CreatedAt); err != nil {
			return nil, err
		}
		if len(before) > 0 {
			_ = json.Unmarshal(before, &v.Before)
		}
		if len(after) > 0 {
			_ = json.Unmarshal(after, &v.After)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
