package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
	"github.com/hutuyee/ShitIDC/internal/security"
)

// ---- login security log & security events (第十五/十七阶段) ----

// RecordLoginAttempt writes one row per login attempt (success or failure).
func (s *Store) RecordLoginAttempt(ctx context.Context, userID int64, email string, success bool, reason, ip, ua string) error {
	var uid any
	if userID > 0 {
		uid = userID
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO user_login_logs(user_id,email,success,reason,ip,user_agent) VALUES($1,$2,$3,$4,NULLIF($5,'')::inet,$6)`,
		uid, email, success, reason, ip, ua)
	return err
}

// SecurityEvent writes to security_events (brute force, rate limits, ...).
func (s *Store) SecurityEvent(ctx context.Context, userID int64, eventType, severity, ip string, metadata map[string]any) error {
	var uid any
	if userID > 0 {
		uid = userID
	}
	_, err := s.DB.Exec(ctx, `INSERT INTO security_events(user_id,event_type,severity,ip,metadata) VALUES($1,$2,$3,NULLIF($4,'')::inet,$5::jsonb)`,
		uid, eventType, severity, ip, metadata)
	return err
}

// ListLoginLogs returns recent login attempts for the admin console.
func (s *Store) ListLoginLogs(ctx context.Context, email string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT l.id,coalesce(l.user_id,0),l.email,l.success,l.reason,coalesce(l.ip::text,''),coalesce(l.user_agent,''),l.created_at
FROM user_login_logs l WHERE ($1='' OR l.email=$1) ORDER BY l.id DESC LIMIT $2`, email, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, uid int64
		var email, reason, ip, ua string
		var success bool
		var created time.Time
		if err := rows.Scan(&id, &uid, &email, &success, &reason, &ip, &ua, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "user_uid": uid, "email": email, "success": success, "reason": reason, "ip": ip, "user_agent": ua, "created_at": created})
	}
	return out, rows.Err()
}

// ---- password change / reset / legacy upgrade (第三/十四阶段) ----

var ErrWrongPassword = errors.New("wrong password")
var ErrAccountLocked = errors.New("account temporarily locked")

// LoginCredentials loads everything the login flow needs in one query:
// user, argon2 hash, legacy (e.g. migrated MagicCube md5) hash, lock state
// and TOTP second factor state (§9).
type LoginCredentials struct {
	User           model.User
	PasswordHash   string
	LegacyHash     string
	FailedAttempts int
	LockedUntil    *time.Time
	TOTPEnabled    bool
	TOTPSecretEnc  string
}

func (s *Store) GetLoginCredentials(ctx context.Context, email string) (LoginCredentials, error) {
	var lc LoginCredentials
	var legacy *string
	var totpEnc *string
	var totpEnabledAt *time.Time
	err := s.DB.QueryRow(ctx, `SELECT u.id,u.public_id::text,u.email,u.status,u.email_verified,u.created_at,us.password_hash,us.legacy_password_hash,us.failed_attempts,us.locked_until,us.totp_secret_encrypted,us.totp_enabled_at
FROM users u JOIN user_security us ON us.user_id=u.id
WHERE u.email=lower($1) AND u.deleted_at IS NULL`, email).Scan(&lc.User.ID, &lc.User.PublicID, &lc.User.Email, &lc.User.Status, &lc.User.EmailVerified, &lc.User.CreatedAt, &lc.PasswordHash, &legacy, &lc.FailedAttempts, &lc.LockedUntil, &totpEnc, &totpEnabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginCredentials{}, ErrNotFound
	}
	if err != nil {
		return LoginCredentials{}, err
	}
	if legacy != nil {
		lc.LegacyHash = *legacy
	}
	if totpEnc != nil && totpEnabledAt != nil {
		lc.TOTPEnabled = true
		lc.TOTPSecretEnc = *totpEnc
	}
	return lc, nil
}

// RegisterLoginFailure bumps the persisted failed-attempt counter and locks
// the account after maxAttempts failures for lockMinutes. Redis throttling
// stays as the first line; this survives restarts and multi-node setups.
func (s *Store) RegisterLoginFailure(ctx context.Context, email string, maxAttempts int, lockMinutes int) error {
	_, err := s.DB.Exec(ctx, `UPDATE user_security SET
failed_attempts = CASE WHEN locked_until IS NOT NULL AND locked_until>now() THEN failed_attempts ELSE failed_attempts+1 END,
locked_until = CASE WHEN failed_attempts+1 >= $2 THEN now()+make_interval(mins=>$3) ELSE locked_until END
WHERE user_id=(SELECT id FROM users WHERE email=lower($1))`, email, maxAttempts, lockMinutes)
	return err
}

// ClearLoginFailures resets the counter after a successful login.
func (s *Store) ClearLoginFailures(ctx context.Context, userID int64) error {
	_, err := s.DB.Exec(ctx, `UPDATE user_security SET failed_attempts=0,locked_until=NULL WHERE user_id=$1`, userID)
	return err
}

// ChangePassword verifies the current password, then rotates the hash.
// All other sessions are revoked so stolen sessions die with the change.
func (s *Store) ChangePassword(ctx context.Context, userID int64, currentPassword, newHash string) error {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var hash string
	if err := tx.QueryRow(ctx, `SELECT password_hash FROM user_security WHERE user_id=$1 FOR UPDATE`, userID).Scan(&hash); err != nil {
		return err
	}
	if hash != currentPassword && !security.VerifyPassword(hash, currentPassword) {
		return ErrWrongPassword
	}
	if _, err := tx.Exec(ctx, `UPDATE user_security SET password_hash=$2,password_changed_at=now(),legacy_password_hash=NULL,failed_attempts=0,locked_until=NULL WHERE user_id=$1`, userID, newHash); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ResetPassword sets a new hash after a verified reset code and clears any
// legacy hash, lockout and TOTP factor (a verified email reset must always
// be able to recover the account, even from a lost authenticator).
func (s *Store) ResetPassword(ctx context.Context, email, newHash string) (int64, error) {
	var userID int64
	err := s.DB.QueryRow(ctx, `UPDATE user_security us SET password_hash=$2,password_changed_at=now(),legacy_password_hash=NULL,failed_attempts=0,locked_until=NULL,totp_secret_encrypted=NULL,totp_enabled_at=NULL
FROM users u WHERE u.id=us.user_id AND u.email=lower($1) RETURNING us.user_id`, email, newHash).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	_, err = s.DB.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, userID)
	return userID, err
}

// UpgradeLegacyPassword replaces the legacy hash with an Argon2id hash after
// a successful legacy verification (first login after MagicCube migration).
func (s *Store) UpgradeLegacyPassword(ctx context.Context, userID int64, newHash string) error {
	_, err := s.DB.Exec(ctx, `UPDATE user_security SET password_hash=$2,legacy_password_hash=NULL,password_changed_at=now() WHERE user_id=$1`, userID, newHash)
	return err
}

// ---- user administration ----

// SetUserStatus enables/disables a user. Disabling revokes all sessions.
func (s *Store) SetUserStatus(ctx context.Context, actorID, targetUserID int64, active bool, requestID string) error {
	status := "active"
	if !active {
		status = "disabled"
	}
	tag, err := s.DB.Exec(ctx, `UPDATE users SET status=$2,updated_at=now() WHERE id=$3 AND status<>$2`, status, status, targetUserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidState
	}
	if !active {
		if _, err := s.DB.Exec(ctx, `UPDATE user_sessions SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL`, targetUserID); err != nil {
			return err
		}
	}
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	_, err = s.DB.Exec(ctx, `INSERT INTO audit_logs(actor_user_id,action,object_type,object_id,request_id,after_data) VALUES($1,'user.status','user',$2,$3,$4)`,
		actor, targetUserID, requestID, map[string]any{"status": status})
	return err
}

// CleanupExpiredSessions purges dead sessions older than the given days.
func (s *Store) CleanupExpiredSessions(ctx context.Context, days int) (int64, error) {
	if days <= 0 {
		days = 7
	}
	tag, err := s.DB.Exec(ctx, `DELETE FROM user_sessions WHERE expires_at<now()-make_interval(days=>$1) OR revoked_at<now()-make_interval(days=>$1)`, days)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
