package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 短信验证码与通道配置（对应魔方 public/plugins/sms/）。
//
// 验证码的存储与邮箱验证码同构：只存哈希、按 (手机号, 用途) 限频、错 5 次锁定。
// 额外多一张发送流水表——短信是花钱的，必须能回答"谁在什么时候给哪个号发了多少条"。

// SmsProvider 是一条短信通道配置。
type SmsProvider struct {
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

// CreateSmsProvider 新增一条短信通道；secretEnc 是加密后的凭据 JSON。
func (s *Store) CreateSmsProvider(ctx context.Context, name, provider string, cfg map[string]string, secretEnc string, isDefault bool) (SmsProvider, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return SmsProvider{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return SmsProvider{}, err
	}
	defer tx.Rollback(ctx)
	if isDefault {
		// 唯一索引只允许一个默认通道，所以先把旧的取消掉。
		if _, err := tx.Exec(ctx, `UPDATE sms_providers SET is_default=FALSE WHERE is_default=TRUE`); err != nil {
			return SmsProvider{}, err
		}
	}
	var v SmsProvider
	var cfgRaw []byte
	err = tx.QueryRow(ctx, `INSERT INTO sms_providers(name,provider,config,secret_encrypted,is_default)
VALUES($1,$2,$3::jsonb,$4,$5)
RETURNING public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at`,
		strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(provider)), string(raw), secretEnc, isDefault).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt)
	if err != nil {
		return SmsProvider{}, err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	if err := tx.Commit(ctx); err != nil {
		return SmsProvider{}, err
	}
	return v, nil
}

// ListSmsProviders 列出全部通道。
func (s *Store) ListSmsProviders(ctx context.Context) ([]SmsProvider, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at
FROM sms_providers ORDER BY is_default DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SmsProvider{}
	for rows.Next() {
		var v SmsProvider
		var cfgRaw []byte
		if err := rows.Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cfgRaw, &v.Config)
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteSmsProvider 删除一条通道。
func (s *Store) DeleteSmsProvider(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM sms_providers WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSmsProviderDefault 把某个通道设为默认（互斥）。
func (s *Store) SetSmsProviderDefault(ctx context.Context, publicID string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE sms_providers SET is_default=FALSE WHERE is_default=TRUE`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE sms_providers SET is_default=TRUE,updated_at=now() WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}

// ActiveSmsProvider 返回默认的启用通道及其加密凭据。
// 没有配置通道时返回 ErrNotFound，调用方据此提示"短信服务未配置"。
func (s *Store) ActiveSmsProvider(ctx context.Context) (SmsProvider, string, error) {
	var v SmsProvider
	var cfgRaw []byte
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,name,provider,config,secret_encrypted<>'',active,is_default,last_ok_at,last_error,created_at,secret_encrypted
FROM sms_providers WHERE active=TRUE ORDER BY is_default DESC, id LIMIT 1`).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.Active, &v.IsDefault, &v.LastOKAt, &v.LastError, &v.CreatedAt, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return SmsProvider{}, "", ErrNotFound
	}
	if err != nil {
		return SmsProvider{}, "", err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	return v, secret, nil
}

// TouchSmsProvider 记录一次发送结果，后台据此显示通道健康度。
func (s *Store) TouchSmsProvider(ctx context.Context, publicID string, ok bool, errText string) {
	if ok {
		_, _ = s.DB.Exec(ctx, `UPDATE sms_providers SET last_ok_at=now(),last_error='',updated_at=now() WHERE public_id=$1`, publicID)
		return
	}
	_, _ = s.DB.Exec(ctx, `UPDATE sms_providers SET last_error=$2,updated_at=now() WHERE public_id=$1`, publicID, errText)
}

// RecordSmsMessage 写一条发送流水。
func (s *Store) RecordSmsMessage(ctx context.Context, phone, purpose, provider, template string, ok bool, errText string, userID int64, ip string) {
	var uid any
	if userID > 0 {
		uid = userID
	}
	_, _ = s.DB.Exec(ctx, `INSERT INTO sms_messages(phone,purpose,provider,template,ok,error,user_id,ip)
VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, phone, purpose, provider, template, ok, errText, uid, ip)
}

// ---- 短信验证码 ----

// PutSmsCode 存一个验证码哈希，并限制 (手机号, 用途) 的重发间隔。
// 与邮箱验证码同构：抢在发短信之前写入，避免"发了但没记下来"。
func (s *Store) PutSmsCode(ctx context.Context, phone, purpose, codeHash string, ttl, resendInterval time.Duration) error {
	phone = normalizePhone(phone)
	var last time.Time
	err := s.DB.QueryRow(ctx, `SELECT created_at FROM sms_verification_codes WHERE phone=$1 AND purpose=$2`, phone, purpose).Scan(&last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if !last.IsZero() && time.Since(last) < resendInterval {
		return ErrCodeRateLimited
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO sms_verification_codes(phone,purpose,code_hash,expires_at,created_at)
VALUES($1,$2,$3,now()+$4::interval,now())
ON CONFLICT(phone,purpose) DO UPDATE SET code_hash=excluded.code_hash,attempts=0,expires_at=excluded.expires_at,created_at=now()`,
		phone, purpose, codeHash, fmtDurationSeconds(ttl))
	return err
}

// ConsumeSmsCode 校验并消费一个验证码。错误时递增尝试次数，5 次后锁定。
func (s *Store) ConsumeSmsCode(ctx context.Context, phone, purpose, codeHash string) error {
	phone = normalizePhone(phone)
	var stored string
	var attempts int
	var expires time.Time
	err := s.DB.QueryRow(ctx, `SELECT code_hash,attempts,expires_at FROM sms_verification_codes WHERE phone=$1 AND purpose=$2`, phone, purpose).Scan(&stored, &attempts, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCodeInvalid
	}
	if err != nil {
		return err
	}
	if time.Now().After(expires) {
		_, _ = s.DB.Exec(ctx, `DELETE FROM sms_verification_codes WHERE phone=$1 AND purpose=$2`, phone, purpose)
		return ErrCodeExpired
	}
	if attempts >= 5 {
		return ErrCodeLocked
	}
	if stored != codeHash {
		_, _ = s.DB.Exec(ctx, `UPDATE sms_verification_codes SET attempts=attempts+1 WHERE phone=$1 AND purpose=$2`, phone, purpose)
		return ErrCodeInvalid
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM sms_verification_codes WHERE phone=$1 AND purpose=$2`, phone, purpose)
	return err
}

// SmsSendStats 是某个手机号最近的发送情况，用于风控与排查盗刷。
type SmsSendStats struct {
	LastMinute int `json:"last_minute"`
	LastHour   int `json:"last_hour"`
	LastDay    int `json:"last_day"`
}

// SmsSendStats 统计某手机号最近 1 分钟 / 1 小时 / 1 天各发了多少条。
func (s *Store) SmsSendStats(ctx context.Context, phone string) (SmsSendStats, error) {
	phone = normalizePhone(phone)
	var st SmsSendStats
	err := s.DB.QueryRow(ctx, `SELECT
  count(*) FILTER (WHERE created_at > now() - interval '1 minute'),
  count(*) FILTER (WHERE created_at > now() - interval '1 hour'),
  count(*) FILTER (WHERE created_at > now() - interval '1 day')
FROM sms_messages WHERE phone=$1 AND ok`, phone).Scan(&st.LastMinute, &st.LastHour, &st.LastDay)
	return st, err
}

// SmsMessageRow 是一条发送流水（后台排查用）。
type SmsMessageRow struct {
	Phone     string    `json:"phone"`
	Purpose   string    `json:"purpose"`
	Provider  string    `json:"provider"`
	Ok        bool      `json:"ok"`
	Error     string    `json:"error"`
	IP        string    `json:"ip"`
	CreatedAt time.Time `json:"created_at"`
}

// ListSmsMessages 列出最近的发送流水，可按手机号过滤。
func (s *Store) ListSmsMessages(ctx context.Context, phone string, limit int) ([]SmsMessageRow, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	phone = normalizePhone(phone)
	rows, err := s.DB.Query(ctx, `SELECT phone,purpose,provider,ok,error,ip,created_at FROM sms_messages
WHERE ($1 = '' OR phone = $1) ORDER BY id DESC LIMIT $2`, phone, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SmsMessageRow{}
	for rows.Next() {
		var v SmsMessageRow
		if err := rows.Scan(&v.Phone, &v.Purpose, &v.Provider, &v.Ok, &v.Error, &v.IP, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// normalizePhone 去掉手机号里的空格与常见分隔符，保证同一号码只对应一行。
// 不做国际区号推断：那是业务策略，交给调用方传入规范化后的号码。
func normalizePhone(phone string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(phone) {
		switch r {
		case ' ', '-', '(', ')':
			continue
		case '+':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SetUserPhone 绑定手机号并标记已验证状态。
func (s *Store) SetUserPhone(ctx context.Context, userID int64, phone string, verified bool) error {
	phone = normalizePhone(phone)
	tag, err := s.DB.Exec(ctx, `UPDATE users SET phone=$2,phone_verified=$3,updated_at=now() WHERE id=$1`, userID, phone, verified)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("该手机号已被其它账号绑定")
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
