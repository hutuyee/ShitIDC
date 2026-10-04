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

// 第三方登录（对应魔方 public/plugins/oauth/）。

// OAuthProviderRow 是一条第三方登录通道配置。
type OAuthProviderRow struct {
	PublicID      string            `json:"id"`
	Name          string            `json:"name"`
	Provider      string            `json:"provider"`
	Config        map[string]string `json:"config"`
	HasSecret     bool              `json:"has_secret"`
	AllowRegister bool              `json:"allow_register"`
	Active        bool              `json:"active"`
	LastError     string            `json:"last_error"`
}

// OAuthStateRow 是一次授权请求的 state。
type OAuthStateRow struct {
	State      string
	Provider   string
	RedirectTo string
	UserID     *int64
}

// ---- 通道配置 ----

// UpsertOAuthProvider 新增或更新一个第三方登录通道（按 provider 唯一）。
func (s *Store) UpsertOAuthProvider(ctx context.Context, name, provider string, cfg map[string]string, secretEnc string, allowRegister bool) (OAuthProviderRow, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return OAuthProviderRow{}, fmt.Errorf("通道类型不能为空")
	}
	if cfg == nil {
		cfg = map[string]string{}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return OAuthProviderRow{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = provider
	}
	var v OAuthProviderRow
	var cfgRaw []byte
	// secret_encrypted 为空时保留原来的值：后台改个显示名不应该把密钥清掉。
	err = s.DB.QueryRow(ctx, `INSERT INTO oauth_providers(name,provider,config,secret_encrypted,allow_register)
VALUES($1,$2,$3::jsonb,$4,$5)
ON CONFLICT (provider) DO UPDATE SET
  name=excluded.name,
  config=excluded.config,
  secret_encrypted=CASE WHEN excluded.secret_encrypted='' THEN oauth_providers.secret_encrypted ELSE excluded.secret_encrypted END,
  allow_register=excluded.allow_register,
  updated_at=now()
RETURNING public_id::text,name,provider,config,secret_encrypted<>'',allow_register,active,last_error`,
		name, provider, string(raw), secretEnc, allowRegister).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.AllowRegister, &v.Active, &v.LastError)
	if err != nil {
		return OAuthProviderRow{}, err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	return v, nil
}

// ListOAuthProviders 列出全部通道（含停用的，供后台管理）。
func (s *Store) ListOAuthProviders(ctx context.Context) ([]OAuthProviderRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,name,provider,config,secret_encrypted<>'',allow_register,active,last_error
FROM oauth_providers ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthProviderRow{}
	for rows.Next() {
		var v OAuthProviderRow
		var cfgRaw []byte
		if err := rows.Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.AllowRegister, &v.Active, &v.LastError); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cfgRaw, &v.Config)
		out = append(out, v)
	}
	return out, rows.Err()
}

// ActiveOAuthProviders 列出启用的通道，供登录页展示按钮。
// 只返回公开信息，不含任何凭据。
func (s *Store) ActiveOAuthProviders(ctx context.Context) ([]OAuthProviderRow, error) {
	all, err := s.ListOAuthProviders(ctx)
	if err != nil {
		return nil, err
	}
	out := []OAuthProviderRow{}
	for _, v := range all {
		if v.Active {
			// 不必把 config 暴露给未登录用户。
			v.Config = nil
			out = append(out, v)
		}
	}
	return out, nil
}

// GetOAuthProviderSecret 取某个通道的加密凭据。
func (s *Store) GetOAuthProviderSecret(ctx context.Context, provider string) (OAuthProviderRow, string, error) {
	var v OAuthProviderRow
	var cfgRaw []byte
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,name,provider,config,secret_encrypted<>'',allow_register,active,last_error,secret_encrypted
FROM oauth_providers WHERE provider=$1 AND active=true`, strings.ToLower(strings.TrimSpace(provider))).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.HasSecret, &v.AllowRegister, &v.Active, &v.LastError, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, "", ErrNotFound
	}
	if err != nil {
		return v, "", err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	return v, secret, nil
}

// SetOAuthProviderActive 启用/停用一个通道。
func (s *Store) SetOAuthProviderActive(ctx context.Context, publicID string, active bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE oauth_providers SET active=$2,updated_at=now() WHERE public_id=$1`, publicID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteOAuthProvider 删除一个通道。已绑定的身份保留（历史可查）。
func (s *Store) DeleteOAuthProvider(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM oauth_providers WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MarkOAuthProviderHealth 记录一次登录结果。
func (s *Store) MarkOAuthProviderHealth(ctx context.Context, provider string, ok bool, errText string) {
	if ok {
		_, _ = s.DB.Exec(ctx, `UPDATE oauth_providers SET last_ok_at=now(),last_error='',updated_at=now() WHERE provider=$1`, provider)
		return
	}
	_, _ = s.DB.Exec(ctx, `UPDATE oauth_providers SET last_error=$2,updated_at=now() WHERE provider=$1`, provider, errText)
}

// ---- state ----

// PutOAuthState 存一个一次性 state。同一 state 重复写入会覆盖（重试场景）。
func (s *Store) PutOAuthState(ctx context.Context, state, provider, redirectTo string, userID int64) error {
	var uid any
	if userID > 0 {
		uid = userID
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO oauth_states(state,provider,redirect_to,user_id,created_at)
VALUES($1,$2,$3,$4,now())
ON CONFLICT (state) DO UPDATE SET provider=excluded.provider,redirect_to=excluded.redirect_to,user_id=excluded.user_id,created_at=now()`,
		state, strings.ToLower(strings.TrimSpace(provider)), redirectTo, uid)
	return err
}

// ConsumeOAuthState 取出并删除一个 state。过期或不存在都返回 ErrNotFound。
//
// 「取出即删除」是防重放的关键：同一个 state 只能用一次。
func (s *Store) ConsumeOAuthState(ctx context.Context, state string, ttl time.Duration) (OAuthStateRow, error) {
	var v OAuthStateRow
	var created time.Time
	err := s.DB.QueryRow(ctx, `DELETE FROM oauth_states WHERE state=$1
RETURNING state,provider,redirect_to,user_id,created_at`, state).
		Scan(&v.State, &v.Provider, &v.RedirectTo, &v.UserID, &created)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	if err != nil {
		return v, err
	}
	// 已删除的记录如果超时就当不存在：不能因为「清理得晚」而放行旧 state。
	if time.Since(created) > ttl {
		return v, ErrNotFound
	}
	return v, nil
}

// PurgeOAuthStates 清掉过期 state（调度器定期调用）。
func (s *Store) PurgeOAuthStates(ctx context.Context, ttl time.Duration) (int64, error) {
	tag, err := s.DB.Exec(ctx, `DELETE FROM oauth_states WHERE created_at < now() - $1::interval`, fmtDurationSeconds(ttl))
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ---- 身份绑定 ----

// OAuthIdentityRow 是一条第三方身份绑定。
type OAuthIdentityRow struct {
	UserID    int64     `json:"-"`
	Provider  string    `json:"provider"`
	Subject   string    `json:"subject"`
	UnionID   string    `json:"union_id,omitempty"`
	Nickname  string    `json:"nickname"`
	AvatarURL string    `json:"avatar_url"`
	CreatedAt time.Time `json:"created_at"`
}

// IdentityInput 是写库用的身份信息。
type IdentityInput struct {
	Provider  string
	Subject   string
	UnionID   string
	Nickname  string
	AvatarURL string
	Email     string
	Raw       map[string]any
}

// oauthNoPasswordSentinel 是 OAuth 自动注册账号的密码占位符。
//
// user_security.password_hash 是 NOT NULL，但这类账号不应该能用密码登录。
// 写一个不可能等于任何 bcrypt 结果的哨兵值：登录时的 bcrypt 比对必然失败，
// 同时 UserHasPassword 会把它当作「没有密码」。
const oauthNoPasswordSentinel = "!oauth"

// FindUserByIdentity 按 (provider, subject) 找已绑定的用户。
//
// 找不到时再试 (provider, union_id)：同一个人在不同微信应用里的 openid 不同，
// 但 unionid 相同，用 unionid 才能把两次登录合并到同一个账号。
func (s *Store) FindUserByIdentity(ctx context.Context, provider, subject, unionID string) (int64, error) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	var userID int64
	err := s.DB.QueryRow(ctx, `SELECT user_id FROM oauth_identities WHERE provider=$1 AND subject=$2`,
		provider, strings.TrimSpace(subject)).Scan(&userID)
	if err == nil {
		return userID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	unionID = strings.TrimSpace(unionID)
	if unionID == "" {
		return 0, ErrNotFound
	}
	err = s.DB.QueryRow(ctx, `SELECT user_id FROM oauth_identities WHERE provider=$1 AND union_id=$2 LIMIT 1`,
		provider, unionID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return userID, err
}

// BindOAuthIdentity 把一个第三方身份绑到用户上。
//
// 冲突语义很关键：同一个 (provider, subject) 已经绑了**别人**时必须报错，
// 不能静默改绑——那等于把别人的账号送出去。绑给同一个人是幂等操作。
func (s *Store) BindOAuthIdentity(ctx context.Context, userID int64, in IdentityInput) error {
	provider := strings.ToLower(strings.TrimSpace(in.Provider))
	subject := strings.TrimSpace(in.Subject)
	if provider == "" || subject == "" {
		return fmt.Errorf("通道与用户标识都不能为空")
	}
	raw, err := json.Marshal(in.Raw)
	if err != nil || in.Raw == nil {
		raw = []byte("{}")
	}
	tag, err := s.DB.Exec(ctx, `INSERT INTO oauth_identities(user_id,provider,subject,union_id,nickname,avatar_url,email,raw)
VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb)
ON CONFLICT (provider,subject) DO UPDATE SET
  union_id=excluded.union_id,
  nickname=excluded.nickname,
  avatar_url=excluded.avatar_url,
  email=excluded.email,
  raw=excluded.raw,
  updated_at=now()
WHERE oauth_identities.user_id = excluded.user_id`,
		userID, provider, subject, strings.TrimSpace(in.UnionID), strings.TrimSpace(in.Nickname),
		strings.TrimSpace(in.AvatarURL), strings.ToLower(strings.TrimSpace(in.Email)), string(raw))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		// ON CONFLICT 的 WHERE 不成立 = 这个身份已经绑在别人身上。
		return fmt.Errorf("该第三方账号已绑定到其它用户")
	}
	return nil
}

// ListOAuthIdentities 列出用户已绑定的第三方身份。
func (s *Store) ListOAuthIdentities(ctx context.Context, userID int64) ([]OAuthIdentityRow, error) {
	rows, err := s.DB.Query(ctx, `SELECT provider,subject,union_id,nickname,avatar_url,created_at
FROM oauth_identities WHERE user_id=$1 ORDER BY provider`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OAuthIdentityRow{}
	for rows.Next() {
		var v OAuthIdentityRow
		if err := rows.Scan(&v.Provider, &v.Subject, &v.UnionID, &v.Nickname, &v.AvatarURL, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.UserID = userID
		out = append(out, v)
	}
	return out, rows.Err()
}

// UnbindOAuthIdentity 解绑一个第三方身份。
//
// 安全约束：**必须至少留一种登录方式**。如果用户没有密码、又只有这一个绑定，
// 解绑后就再也登不进来了，所以直接拒绝。
func (s *Store) UnbindOAuthIdentity(ctx context.Context, userID int64, provider string) error {
	provider = strings.ToLower(strings.TrimSpace(provider))
	hasPassword, err := s.UserHasPassword(ctx, userID)
	if err != nil {
		return err
	}
	if !hasPassword {
		var count int
		if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM oauth_identities WHERE user_id=$1`, userID).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return fmt.Errorf("这是账号唯一的登录方式，请先设置密码再解绑")
		}
	}
	tag, err := s.DB.Exec(ctx, `DELETE FROM oauth_identities WHERE user_id=$1 AND provider=$2`, userID, provider)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UserHasPassword 报告用户是否设置了可用密码。
// OAuth 自动注册的账号写的是哨兵值，视为「没有密码」。
func (s *Store) UserHasPassword(ctx context.Context, userID int64) (bool, error) {
	var set bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM user_security WHERE user_id=$1 AND password_hash <> '' AND password_hash <> '!oauth')`, userID).Scan(&set)
	return set, err
}

// CreateOAuthUser 为第三方登录创建一个没有可用密码的账号。
func (s *Store) CreateOAuthUser(ctx context.Context, email, nickname string, emailVerified bool) (int64, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return 0, fmt.Errorf("第三方平台没有返回邮箱，无法自动创建账号")
	}
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var userID int64
	var publicID string
	err = tx.QueryRow(ctx, `INSERT INTO users(email,email_verified) VALUES($1,$2)
ON CONFLICT (email) DO NOTHING
RETURNING id,public_id::text`, email, emailVerified).Scan(&userID, &publicID)
	if errors.Is(err, pgx.ErrNoRows) {
		// 邮箱已存在：交给上层走「绑定到已有账号」流程，绝不能静默登录别人的号。
		return 0, fmt.Errorf("该邮箱已被注册，请先用密码登录后再绑定第三方账号")
	}
	if err != nil {
		return 0, err
	}
	_ = publicID
	if _, err := tx.Exec(ctx, `INSERT INTO user_security(user_id,password_hash) VALUES($1,$2)`, userID, oauthNoPasswordSentinel); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO wallet_accounts(user_id,currency) VALUES($1,'CNY')`, userID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO user_roles(user_id,role_id) SELECT $1,id FROM roles WHERE name='customer'`, userID); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return userID, nil
}
