package store

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

var (
	ErrCodeInvalid      = errors.New("verification code invalid")
	ErrCodeExpired      = errors.New("verification code expired")
	ErrCodeLocked       = errors.New("verification code locked")
	ErrCodeRateLimited  = errors.New("verification code requested too often")
	ErrAmountMismatch   = errors.New("paid amount mismatch")
	ErrAlreadyCompleted = errors.New("payment already completed")
)

// GetUserByIDOrPublicID resolves a user by sequential numeric ID (e.g. "1")
// or by public UUID, whichever the caller provides.
func (s *Store) GetUserByIDOrPublicID(ctx context.Context, ref string) (model.User, error) {
	ref = strings.TrimSpace(ref)
	if ref != "" && isAllDigits(ref) {
		if id, err := strconv.ParseInt(ref, 10, 64); err == nil {
			if u, err := s.GetUserByID(ctx, id); err == nil {
				return u, nil
			}
		}
	}
	return s.GetUserByPublicID(ctx, ref)
}

func isAllDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return len(s) > 0
}

func (s *Store) ListUsers(ctx context.Context, query string, limit int) ([]model.AdminUser, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT u.id,u.public_id::text,u.email,u.status,u.email_verified,u.created_at,
 COALESCE(w.balance_cents,0),COALESCE(w.currency,'CNY'),
 (SELECT count(*) FROM orders o WHERE o.user_id=u.id),
 (SELECT max(s.last_seen_at) FROM user_sessions s WHERE s.user_id=u.id)
FROM users u
LEFT JOIN wallet_accounts w ON w.user_id=u.id AND w.currency='CNY'
WHERE u.deleted_at IS NULL AND ($1='' OR u.email ILIKE '%'||$1||'%' OR u.id::text=$1 OR u.public_id::text=$1)
ORDER BY u.id DESC LIMIT $2`, strings.TrimSpace(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.AdminUser{}
	for rows.Next() {
		var v model.AdminUser
		if err := rows.Scan(&v.UID, &v.PublicID, &v.Email, &v.Status, &v.EmailVerified, &v.CreatedAt, &v.BalanceCents, &v.Currency, &v.OrderCount, &v.LastLoginAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// PutEmailCode stores a hashed verification code. It enforces a minimum
// resend interval per (email, purpose) to stop mail bombing.
func (s *Store) PutEmailCode(ctx context.Context, email, purpose, codeHash string, ttl, resendInterval time.Duration) error {
	var last time.Time
	err := s.DB.QueryRow(ctx, `SELECT created_at FROM email_verification_codes WHERE email=$1 AND purpose=$2`, email, purpose).Scan(&last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if !last.IsZero() && time.Since(last) < resendInterval {
		return ErrCodeRateLimited
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO email_verification_codes(email,purpose,code_hash,expires_at,created_at)
VALUES($1,$2,$3,now()+$4::interval,now())
ON CONFLICT(email,purpose) DO UPDATE SET code_hash=excluded.code_hash,attempts=0,expires_at=excluded.expires_at,created_at=now()`,
		email, purpose, codeHash, fmtDurationSeconds(ttl))
	return err
}

func fmtDurationSeconds(d time.Duration) string {
	return strconv.Itoa(int(d.Seconds())) + " seconds"
}

func (s *Store) ConsumeEmailCode(ctx context.Context, email, purpose, codeHash string) error {
	var stored string
	var attempts int
	var expires time.Time
	err := s.DB.QueryRow(ctx, `SELECT code_hash,attempts,expires_at FROM email_verification_codes WHERE email=$1 AND purpose=$2`, email, purpose).Scan(&stored, &attempts, &expires)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrCodeInvalid
	}
	if err != nil {
		return err
	}
	if time.Now().After(expires) {
		_, _ = s.DB.Exec(ctx, `DELETE FROM email_verification_codes WHERE email=$1 AND purpose=$2`, email, purpose)
		return ErrCodeExpired
	}
	if attempts >= 5 {
		return ErrCodeLocked
	}
	if stored != codeHash {
		_, _ = s.DB.Exec(ctx, `UPDATE email_verification_codes SET attempts=attempts+1 WHERE email=$1 AND purpose=$2`, email, purpose)
		return ErrCodeInvalid
	}
	_, err = s.DB.Exec(ctx, `DELETE FROM email_verification_codes WHERE email=$1 AND purpose=$2`, email, purpose)
	return err
}

func (s *Store) SetEmailVerified(ctx context.Context, userID int64, verified bool) error {
	_, err := s.DB.Exec(ctx, `UPDATE users SET email_verified=$2,updated_at=now() WHERE id=$1`, userID, verified)
	return err
}
