package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 邮件通道配置（对应魔方 public/plugins/mail/）。
// 结构与 sms_providers 对齐：多条通道、一个默认、凭据加密、健康度留痕。

// MailProvider 是一条邮件通道配置。
type MailProvider struct {
	PublicID  string            `json:"id"`
	Name      string            `json:"name"`
	Provider  string            `json:"provider"`
	Config    map[string]string `json:"config"`
	HasSecret bool              `json:"has_secret"`
	Active    bool              `json:"active"`
	IsDefault bool              `json:"is_default"`
	LastOKAt  *time.Time        `json:"last_ok_at,omitempty"`
	LastError string            `json:"last_error"`
	CreatedAt time.Time         `json:"created_at"`
}

// CreateMailProvider 新增一条邮件通道；secretEnc 是加密后的凭据 JSON。
func (s *Store) CreateMailProvider(ctx context.Context, name, provider string, cfg map[string]string, secretEnc string, isDefault bool) (MailProvider, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return MailProvider{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return MailProvider{}, err
	}
	defer tx.Rollback(ctx)
	if isDefault {
		if _, err := tx.Exec(ctx, `UPDATE mail_providers SET is_default=FALSE WHERE is_default=TRUE`); err != nil {
			return MailProvider{}, err
		}
	}
	var v MailProvider
	var cfgRaw []byte
	err = tx.QueryRow(ctx, `INSERT INTO mail_providers(name,provider,config,secret_encrypted,is_default)
VALUES($1,$2,$3::jsonb,$4,$5)
RETURNING public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at`,
		strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(provider)), string(raw), secretEnc, isDefault).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt)
	if err != nil {
		return MailProvider{}, err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	if err := tx.Commit(ctx); err != nil {
		return MailProvider{}, err
	}
	return v, nil
}

// ListMailProviders 列出全部通道。
func (s *Store) ListMailProviders(ctx context.Context) ([]MailProvider, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at
FROM mail_providers ORDER BY is_default DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MailProvider{}
	for rows.Next() {
		var v MailProvider
		var cfgRaw []byte
		if err := rows.Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cfgRaw, &v.Config)
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetMailProvider 按公开 ID 取一条通道，同时返回加密凭据（仅服务端使用）。
func (s *Store) GetMailProvider(ctx context.Context, publicID string) (MailProvider, string, error) {
	var v MailProvider
	var cfgRaw []byte
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at,secret_encrypted
FROM mail_providers WHERE public_id=$1`, publicID).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return MailProvider{}, "", ErrNotFound
	}
	if err != nil {
		return MailProvider{}, "", err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	return v, secret, nil
}

// DeleteMailProvider 删除一条通道。
func (s *Store) DeleteMailProvider(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM mail_providers WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMailProviderDefault 把某个通道设为默认（互斥）。
func (s *Store) SetMailProviderDefault(ctx context.Context, publicID string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE mail_providers SET is_default=FALSE WHERE is_default=TRUE`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE mail_providers SET is_default=TRUE,updated_at=now() WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// ActiveMailProvider 返回默认的启用通道及其加密凭据。
// 没有配置通道时返回 ErrNotFound，调用方回退到内置 SMTP。
func (s *Store) ActiveMailProvider(ctx context.Context) (MailProvider, string, error) {
	var v MailProvider
	var cfgRaw []byte
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at,secret_encrypted
FROM mail_providers WHERE active=TRUE ORDER BY is_default DESC, id LIMIT 1`).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return MailProvider{}, "", ErrNotFound
	}
	if err != nil {
		return MailProvider{}, "", err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	return v, secret, nil
}

// TouchMailProvider 记录一次发送结果（健康度与最近错误）。
func (s *Store) TouchMailProvider(ctx context.Context, publicID string, ok bool, errText string) {
	if strings.TrimSpace(publicID) == "" {
		return
	}
	if ok {
		_, _ = s.DB.Exec(ctx, `UPDATE mail_providers SET last_ok_at=now(),last_error='',updated_at=now() WHERE public_id=$1`, publicID)
		return
	}
	_, _ = s.DB.Exec(ctx, `UPDATE mail_providers SET last_error=$2,updated_at=now() WHERE public_id=$1`, publicID, errText)
}
