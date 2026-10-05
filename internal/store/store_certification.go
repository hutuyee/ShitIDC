package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/security"
)

// 实名认证（对应魔方 public/plugins/certification/）。
//
// 隐私处理的硬性约束：**身份证号与真实姓名绝不落明文**。
//   * 展示用掩码：张*三 / 110***********1234
//   * 去重用哈希：HMAC-SHA256(master, 姓名) 与 HMAC-SHA256(master, 证件号)
// 用 HMAC 而不是裸 SHA256：证件号空间有限（约 10^17 但有大量规律），
// 裸哈希可以被穷举反查，加盐（master key）就不会。

// Certification 是一条实名认证记录。
type Certification struct {
	PublicID       string     `json:"id"`
	UserID         int64      `json:"-"`
	UserPublicID   string     `json:"user_id"`
	UserEmail      string     `json:"user_email,omitempty"`
	Status         string     `json:"status"`
	RealNameMasked string     `json:"real_name_masked"`
	IDType         string     `json:"id_type"`
	IDNumberMasked string     `json:"id_number_masked"`
	Gender         string     `json:"gender,omitempty"`
	BirthDate      *time.Time `json:"birth_date,omitempty"`
	Provider       string     `json:"provider"`
	VerifiedBy     string     `json:"verified_by,omitempty"`
	ProviderRef    string     `json:"-"`
	ProviderURL    string     `json:"-"`
	RejectReason   string     `json:"reject_reason,omitempty"`
	SubmittedAt    time.Time  `json:"submitted_at"`
	ReviewedAt     *time.Time `json:"reviewed_at,omitempty"`
}

// CertificationInput 是一次实名提交的原始材料（仅在内存中存在）。
type CertificationInput struct {
	RealName string
	IDNumber string
	IDType   string
	// Provider 是做出判定的通道标识。
	Provider string
	// Approved 表示通道判定「一致」；false 时落 pending 等人工审核。
	Approved bool
	// VerifiedBy 记录判定来源（通道名或 admin）。
	VerifiedBy string
	Gender     string
	BirthDate  string
	// ProviderRef / ProviderURL 保存扫码类通道的中间凭证与二维码地址，仅用于轮询。
	ProviderRef string
	ProviderURL string
}

// CertHash 用 master key 对实名信息做 HMAC，得到不可反查的去重指纹。
// 导出它是为了让 API 层与测试都能用同一套算法。
func CertHash(master []byte, value string) string {
	return security.HMACSHA256Hex(master, strings.TrimSpace(value))
}

// SubmitCertification 落库一次实名提交。
//
// 同一用户重复提交会覆盖上一条（user_id 上有唯一约束）——这是有意的：
// 用户填错了应该能改，而不是被一条错误记录锁死。但已经 approved 的记录
// 不允许被静默覆盖，必须走「重新认证」流程（由 API 层先驳回）。
func (s *Store) SubmitCertification(ctx context.Context, userID int64, in CertificationInput) (Certification, error) {
	if strings.TrimSpace(in.RealName) == "" || strings.TrimSpace(in.IDNumber) == "" {
		return Certification{}, fmt.Errorf("姓名与证件号都不能为空")
	}
	if len(s.MasterKey) == 0 {
		return Certification{}, fmt.Errorf("服务器未配置 MASTER_KEY_BASE64，无法安全存储实名信息")
	}
	idType := strings.TrimSpace(in.IDType)
	if idType == "" {
		idType = "idcard"
	}
	status := "pending"
	if in.Approved {
		status = "approved"
	}

	var birth any
	if in.BirthDate != "" {
		birth = in.BirthDate
	}
	var reviewedAt any
	if in.Approved {
		reviewedAt = time.Now()
	}

	var v Certification
	err := s.DB.QueryRow(ctx, `INSERT INTO certifications(
  user_id,status,real_name_masked,id_type,id_number_masked,name_hash,id_number_hash,
  gender,birth_date,provider,verified_by,provider_ref,provider_url,submitted_at,reviewed_at,updated_at)
-- 这些列都是 NOT NULL DEFAULT ''：必须用 COALESCE 兜底空串，
-- 写成 NULLIF(...) 会把空值变 NULL 直接撞 23502。
VALUES($1,$2,$3,$4,$5,$6,$7,COALESCE($8,''),$9::date,COALESCE($10,''),COALESCE($11,''),COALESCE($12,''),COALESCE($13,''),now(),$14,now())
ON CONFLICT (user_id) DO UPDATE SET
  status=excluded.status,
  real_name_masked=excluded.real_name_masked,
  id_type=excluded.id_type,
  id_number_masked=excluded.id_number_masked,
  name_hash=excluded.name_hash,
  id_number_hash=excluded.id_number_hash,
  gender=excluded.gender,
  birth_date=excluded.birth_date,
  provider=excluded.provider,
  verified_by=excluded.verified_by,
  provider_ref=excluded.provider_ref,
  provider_url=excluded.provider_url,
  reject_reason='',
  submitted_at=now(),
  reviewed_at=excluded.reviewed_at,
  updated_at=now()
RETURNING public_id::text,user_id,status,real_name_masked,id_type,id_number_masked,gender,birth_date,provider,verified_by,provider_ref,provider_url,reject_reason,submitted_at,reviewed_at`,
		userID, status, certificationMaskName(in.RealName), idType, certificationMaskID(in.IDNumber),
		CertHash(s.MasterKey, in.RealName), CertHash(s.MasterKey, in.IDNumber),
		in.Gender, birth, in.Provider, in.VerifiedBy, in.ProviderRef, in.ProviderURL, reviewedAt).
		Scan(&v.PublicID, &v.UserID, &v.Status, &v.RealNameMasked, &v.IDType, &v.IDNumberMasked,
			&v.Gender, &v.BirthDate, &v.Provider, &v.VerifiedBy, &v.ProviderRef, &v.ProviderURL, &v.RejectReason, &v.SubmittedAt, &v.ReviewedAt)
	if err != nil {
		if isUniqueViolation(err) {
			// 唯一索引只建在 approved 上：说明这个证件号已经绑定过别的账号。
			return Certification{}, fmt.Errorf("该证件号已被其它账号实名认证")
		}
		return Certification{}, err
	}
	return v, nil
}

// GetCertification 读一个用户的实名记录；没有则返回 ErrNotFound。
func (s *Store) GetCertification(ctx context.Context, userID int64) (Certification, error) {
	var v Certification
	err := s.DB.QueryRow(ctx, `SELECT c.public_id::text,c.user_id,c.status,c.real_name_masked,c.id_type,c.id_number_masked,
  c.gender,c.birth_date,c.provider,c.verified_by,c.provider_ref,c.provider_url,c.reject_reason,c.submitted_at,c.reviewed_at
FROM certifications c WHERE c.user_id=$1`, userID).
		Scan(&v.PublicID, &v.UserID, &v.Status, &v.RealNameMasked, &v.IDType, &v.IDNumberMasked,
			&v.Gender, &v.BirthDate, &v.Provider, &v.VerifiedBy, &v.ProviderRef, &v.ProviderURL, &v.RejectReason, &v.SubmittedAt, &v.ReviewedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// IsCertified 报告用户是否已通过实名认证（下单前校验用）。
func (s *Store) IsCertified(ctx context.Context, userID int64) (bool, error) {
	var ok bool
	err := s.DB.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM certifications WHERE user_id=$1 AND status='approved')`, userID).Scan(&ok)
	return ok, err
}

// CertificationRequired 读站点设置「是否必须实名才能下单」。
func (s *Store) CertificationRequired(ctx context.Context) (bool, error) {
	var raw []byte
	err := s.DB.QueryRow(ctx, `SELECT value FROM system_settings WHERE key='certification_required'`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var v bool
	if err := json.Unmarshal(raw, &v); err != nil {
		// 设置被写成字符串 "true" 之类也认。
		var str string
		if err2 := json.Unmarshal(raw, &str); err2 == nil {
			return strings.EqualFold(strings.TrimSpace(str), "true"), nil
		}
		return false, nil
	}
	return v, nil
}

// ReviewCertification 管理员审核：approve=false 时记录驳回原因。
func (s *Store) ReviewCertification(ctx context.Context, certPublicID string, approve bool, reason string, reviewerID int64) error {
	status := "rejected"
	if approve {
		status = "approved"
	}
	var rid any
	if reviewerID > 0 {
		rid = reviewerID
	}
	tag, err := s.DB.Exec(ctx, `UPDATE certifications
SET status=$2,reject_reason=CASE WHEN $2='rejected' THEN $3 ELSE '' END,
    verified_by='admin',reviewed_at=now(),reviewed_by=$4,updated_at=now()
WHERE public_id=$1`, certPublicID, status, strings.TrimSpace(reason), rid)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("该证件号已被其它账号实名认证")
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ResolveCertification 把一条 pending 的扫码认证写成终态（轮询命中时调用）。
// 只动 pending：approved / rejected 已成定局，不能被后到的轮询改写。
func (s *Store) ResolveCertification(ctx context.Context, userID int64, approved bool, message string) error {
	status := "rejected"
	if approved {
		status = "approved"
	}
	var reviewedAt any
	if approved {
		reviewedAt = time.Now()
	}
	tag, err := s.DB.Exec(ctx, `UPDATE certifications
SET status=$2, reject_reason=CASE WHEN $2='rejected' THEN $3 ELSE '' END,
    verified_by=CASE WHEN $2='approved' THEN provider ELSE verified_by END,
    reviewed_at=$4, updated_at=now()
WHERE user_id=$1 AND status='pending'`, userID, status, strings.TrimSpace(message), reviewedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return fmt.Errorf("该证件号已被其它账号实名认证")
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListCertifications 列出实名记录，可按状态过滤。
func (s *Store) ListCertifications(ctx context.Context, status string, limit int) ([]Certification, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT c.public_id::text,c.user_id,u.public_id::text,u.email,c.status,c.real_name_masked,
  c.id_type,c.id_number_masked,c.gender,c.birth_date,c.provider,c.verified_by,c.reject_reason,c.submitted_at,c.reviewed_at
FROM certifications c JOIN users u ON u.id=c.user_id
WHERE ($1='' OR c.status=$1) ORDER BY c.submitted_at DESC LIMIT $2`, strings.TrimSpace(status), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Certification{}
	for rows.Next() {
		var v Certification
		if err := rows.Scan(&v.PublicID, &v.UserID, &v.UserPublicID, &v.UserEmail, &v.Status, &v.RealNameMasked,
			&v.IDType, &v.IDNumberMasked, &v.Gender, &v.BirthDate, &v.Provider, &v.VerifiedBy, &v.RejectReason, &v.SubmittedAt, &v.ReviewedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// certificationMaskName / certificationMaskID 是本包的掩码实现。
// 之所以在这里再写一份而不是 import certification 包：store 不应该依赖上层业务包
// （会形成循环），而掩码规则本身很短且必须稳定。
func certificationMaskName(name string) string {
	r := []rune(strings.TrimSpace(name))
	switch len(r) {
	case 0:
		return ""
	case 1:
		return string(r)
	case 2:
		return string(r[0]) + "*"
	default:
		return string(r[0]) + strings.Repeat("*", len(r)-2) + string(r[len(r)-1])
	}
}

func certificationMaskID(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 7 {
		if len(id) == 0 {
			return ""
		}
		return strings.Repeat("*", len(id))
	}
	return id[:3] + strings.Repeat("*", len(id)-7) + id[len(id)-4:]
}

// CertificationProvider 是一条实名通道配置。
type CertificationProvider struct {
	PublicID  string            `json:"id"`
	Name      string            `json:"name"`
	Provider  string            `json:"provider"`
	Config    map[string]string `json:"config"`
	Active    bool              `json:"active"`
	IsDefault bool              `json:"is_default"`
	HasSecret bool              `json:"has_secret"`
	LastOKAt  *time.Time        `json:"last_ok_at,omitempty"`
	LastError string            `json:"last_error"`
}

// ActiveCertificationProvider 返回默认启用的实名通道及其加密凭据。
func (s *Store) ActiveCertificationProvider(ctx context.Context) (CertificationProvider, string, error) {
	var v CertificationProvider
	var cfgRaw []byte
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT public_id::text,name,provider,config,active,is_default,secret_encrypted
FROM certification_providers WHERE active=TRUE ORDER BY is_default DESC, id LIMIT 1`).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.Active, &v.IsDefault, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, "", ErrNotFound
	}
	if err != nil {
		return v, "", err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	return v, secret, nil
}

// MarkCertificationProviderHealth 记录一次核验结果，后台据此显示通道健康度。
func (s *Store) MarkCertificationProviderHealth(ctx context.Context, publicID string, ok bool, errText string) {
	if publicID == "" {
		return
	}
	if ok {
		_, _ = s.DB.Exec(ctx, `UPDATE certification_providers SET last_ok_at=now(),last_error='',updated_at=now() WHERE public_id=$1`, publicID)
		return
	}
	_, _ = s.DB.Exec(ctx, `UPDATE certification_providers SET last_error=$2,updated_at=now() WHERE public_id=$1`, publicID, errText)
}

// ---- 实名通道管理（管理端）----

// CreateCertificationProvider 新增一条实名通道；secretEnc 是加密后的凭据 JSON。
func (s *Store) CreateCertificationProvider(ctx context.Context, name, provider string, cfg map[string]string, secretEnc string, isDefault bool) (CertificationProvider, error) {
	if cfg == nil {
		cfg = map[string]string{}
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return CertificationProvider{}, err
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return CertificationProvider{}, err
	}
	defer tx.Rollback(ctx)
	if isDefault {
		// 唯一索引只允许一个默认通道，所以先把旧的取消掉。
		if _, err := tx.Exec(ctx, `UPDATE certification_providers SET is_default=FALSE WHERE is_default=TRUE`); err != nil {
			return CertificationProvider{}, err
		}
	}
	var v CertificationProvider
	var cfgRaw []byte
	err = tx.QueryRow(ctx, `INSERT INTO certification_providers(name,provider,config,secret_encrypted,is_default)
VALUES($1,$2,$3::jsonb,$4,$5)
RETURNING public_id::text,name,provider,config,active,is_default`,
		strings.TrimSpace(name), strings.ToLower(strings.TrimSpace(provider)), string(raw), secretEnc, isDefault).
		Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.Active, &v.IsDefault)
	if err != nil {
		return CertificationProvider{}, err
	}
	_ = json.Unmarshal(cfgRaw, &v.Config)
	if err := tx.Commit(ctx); err != nil {
		return CertificationProvider{}, err
	}
	return v, nil
}

// ListCertificationProviders 列出全部实名通道。
func (s *Store) ListCertificationProviders(ctx context.Context) ([]CertificationProvider, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,name,provider,config,active,is_default,secret_encrypted<>'',last_ok_at,last_error
FROM certification_providers ORDER BY is_default DESC, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CertificationProvider{}
	for rows.Next() {
		var v CertificationProvider
		var cfgRaw []byte
		if err := rows.Scan(&v.PublicID, &v.Name, &v.Provider, &cfgRaw, &v.Active, &v.IsDefault, &v.HasSecret, &v.LastOKAt, &v.LastError); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(cfgRaw, &v.Config)
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteCertificationProvider 删除一条实名通道。
func (s *Store) DeleteCertificationProvider(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM certification_providers WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCertificationProviderDefault 把某个通道设为默认（互斥）并激活。
func (s *Store) SetCertificationProviderDefault(ctx context.Context, publicID string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE certification_providers SET is_default=FALSE WHERE is_default=TRUE`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE certification_providers SET is_default=TRUE,active=TRUE,updated_at=now() WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return tx.Commit(ctx)
}
