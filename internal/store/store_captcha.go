package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 人机验证通道配置（对应魔方 public/plugins/captcha/）。
// 结构与 sms_providers / mail_providers 对齐：多条通道、一个默认、凭据加密、健康度留痕。

// CaptchaProvider 是一条人机验证通道配置。
type CaptchaProvider struct {
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

// CreateCaptchaProvider 新增一条人机验证通道；secretEnc 是加密后的凭据 JSON。
func (s *Store) CreateCaptchaProvider(ctx context.Context, name, provider string, cfg map[string]string, secretEnc string, isDefault bool) (CaptchaProvider, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return CaptchaProvider{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return CaptchaProvider{}, err
	}
	defer tx.Rollback(ctx)
	if isDefault {
		if _, err := tx.Exec(ctx, `UPDATE captcha_providers SET is_default=FALSE WHERE is_default=TRUE`); err != nil {
			return CaptchaProvider{}, err
		}
	}
	var v CaptchaProvider
	var cfgRaw []byte
	err = tx.QueryRow(ctx, `INSERT INTO captcha_providers(name,provider,config,secret_encrypted,is_default)
VALUES($1,$2,$3::jsonb,$4,$5)
RETURNING public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at`,
		strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(provider)), string(raw), secretEnc, isDefault).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt)
	if err != nil {
		return CaptchaProvider{}, err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	if err := tx.Commit(ctx); err != nil {
		return CaptchaProvider{}, err
	}
	return v, nil
}

// ListCaptchaProviders 列出全部通道。
func (s *Store) ListCaptchaProviders(ctx context.Context) ([]CaptchaProvider, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at
FROM captcha_providers ORDER BY is_default DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CaptchaProvider{}
	for rows.Next() {
		var v CaptchaProvider
		var cfgRaw []byte
		if err := rows.Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cfgRaw, &v.Config)
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetCaptchaProvider 按公开 ID 取一条通道，同时返回加密凭据（仅服务端使用）。
func (s *Store) GetCaptchaProvider(ctx context.Context, publicID string) (CaptchaProvider, string, error) {
	var v CaptchaProvider
	var cfgRaw []byte
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at,secret_encrypted
FROM captcha_providers WHERE public_id=$1`, publicID).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return CaptchaProvider{}, "", ErrNotFound
	}
	if err != nil {
		return CaptchaProvider{}, "", err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	return v, secret, nil
}

// DeleteCaptchaProvider 删除一条通道。
func (s *Store) DeleteCaptchaProvider(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM captcha_providers WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCaptchaProviderDefault 把某个通道设为默认（互斥）。
func (s *Store) SetCaptchaProviderDefault(ctx context.Context, publicID string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE captcha_providers SET is_default=FALSE WHERE is_default=TRUE`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE captcha_providers SET is_default=TRUE,updated_at=now() WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// ActiveCaptchaProvider 返回默认的启用通道及其加密凭据。
// 没有配置通道时返回 ErrNotFound，调用方回退内置图形验证码。
func (s *Store) ActiveCaptchaProvider(ctx context.Context) (CaptchaProvider, string, error) {
	var v CaptchaProvider
	var cfgRaw []byte
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at,secret_encrypted
FROM captcha_providers WHERE active=TRUE ORDER BY is_default DESC, id LIMIT 1`).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return CaptchaProvider{}, "", ErrNotFound
	}
	if err != nil {
		return CaptchaProvider{}, "", err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	return v, secret, nil
}

// TouchCaptchaProvider 记录一次校验结果（健康度与最近错误）。
func (s *Store) TouchCaptchaProvider(ctx context.Context, publicID string, ok bool, errText string) {
	if strings.TrimSpace(publicID) == "" {
		return
	}
	if ok {
		_, _ = s.DB.Exec(ctx, `UPDATE captcha_providers SET last_ok_at=now(),last_error='',updated_at=now() WHERE public_id=$1`, publicID)
		return
	}
	_, _ = s.DB.Exec(ctx, `UPDATE captcha_providers SET last_error=$2,updated_at=now() WHERE public_id=$1`, publicID, errText)
}
