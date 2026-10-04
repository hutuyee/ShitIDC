package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// 魔方 (ZJMF) upstream access keys — see migrations/009_magiccube_upstream.sql.
//
// A 魔方财务 server module authenticates with the 魔方 signature scheme
// (time + random + token, md5, upper-case). The token itself is never sent, so
// verifying a request requires the original secret; unlike api_tokens (which
// only keeps an HMAC and can therefore never be verified this way) the 魔方 key
// is stored AES-256-GCM encrypted with MASTER_KEY and decrypted only to check
// the signature.

// UpstreamKey is a persisted 魔方 upstream credential.
type UpstreamKey struct {
	PublicID   string     `json:"id"`
	Name       string     `json:"name"`
	KeyID      string     `json:"key_id"`
	UserUID    int64      `json:"user_uid"`
	UserEmail  string     `json:"user_email,omitempty"`
	Scopes     []string   `json:"scopes"`
	Active     bool       `json:"active"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	LastUsedIP string     `json:"last_used_ip,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// CreateUpstreamKey stores one 魔方 access key; secretEnc must already be
// encrypted with the master key.
func (s *Store) CreateUpstreamKey(ctx context.Context, userID int64, name, keyID, secretEnc string, scopes []string) (UpstreamKey, error) {
	if len(scopes) == 0 {
		scopes = []string{"product.read", "service.read", "service.operate", "order.read", "order.write"}
	}
	var v UpstreamKey
	err := s.DB.QueryRow(ctx, `INSERT INTO upstream_keys(user_id,name,key_id,secret_enc,scopes) VALUES($1,$2,$3,$4,$5)
RETURNING public_id::text,name,key_id,scopes,active,last_used_at,coalesce(host(last_used_ip),''),last_error,created_at`,
		userID, strings.TrimSpace(name), keyID, secretEnc, scopes).
		Scan(&v.PublicID, &v.Name, &v.KeyID, &v.Scopes, &v.Active, &v.LastUsedAt, &v.LastUsedIP, &v.LastError, &v.CreatedAt)
	if err != nil {
		if isUniqueViolation(err) {
			return UpstreamKey{}, fmt.Errorf("该密钥标识已存在")
		}
		return UpstreamKey{}, err
	}
	v.UserUID = userID
	return v, nil
}

// ListUpstreamKeys returns every 魔方 key with its owner.
func (s *Store) ListUpstreamKeys(ctx context.Context) ([]UpstreamKey, error) {
	rows, err := s.DB.Query(ctx, `SELECT k.public_id::text,k.name,k.key_id,k.user_id,u.email,k.scopes,k.active,k.last_used_at,coalesce(host(k.last_used_ip),''),k.last_error,k.created_at
FROM upstream_keys k JOIN users u ON u.id=k.user_id ORDER BY k.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UpstreamKey{}
	for rows.Next() {
		var v UpstreamKey
		if err := rows.Scan(&v.PublicID, &v.Name, &v.KeyID, &v.UserUID, &v.UserEmail, &v.Scopes, &v.Active, &v.LastUsedAt, &v.LastUsedIP, &v.LastError, &v.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// DeleteUpstreamKey revokes a 魔方 key.
func (s *Store) DeleteUpstreamKey(ctx context.Context, publicID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM upstream_keys WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetUpstreamKeyActive enables or disables a key without deleting it.
func (s *Store) SetUpstreamKeyActive(ctx context.Context, publicID string, active bool) error {
	tag, err := s.DB.Exec(ctx, `UPDATE upstream_keys SET active=$2,updated_at=now() WHERE public_id=$1`, publicID, active)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpstreamKeySecret resolves an active key by its key_id and returns the owner
// plus the encrypted secret for signature verification. The caller decrypts.
func (s *Store) UpstreamKeySecret(ctx context.Context, keyID string) (UpstreamKey, string, error) {
	var v UpstreamKey
	var secretEnc string
	err := s.DB.QueryRow(ctx, `SELECT k.public_id::text,k.name,k.key_id,k.user_id,u.email,k.scopes,k.active,k.last_used_at,coalesce(host(k.last_used_ip),''),k.last_error,k.created_at,k.secret_enc
FROM upstream_keys k JOIN users u ON u.id=k.user_id
WHERE k.key_id=$1 AND k.active=true AND u.status='active'`, strings.TrimSpace(keyID)).
		Scan(&v.PublicID, &v.Name, &v.KeyID, &v.UserUID, &v.UserEmail, &v.Scopes, &v.Active, &v.LastUsedAt, &v.LastUsedIP, &v.LastError, &v.CreatedAt, &secretEnc)
	if errors.Is(err, pgx.ErrNoRows) {
		return UpstreamKey{}, "", ErrNotFound
	}
	if err != nil {
		return UpstreamKey{}, "", err
	}
	return v, secretEnc, nil
}

// TouchUpstreamKey records successful use (or the failure reason) so the admin
// console can show whether a 魔方 install is actually talking to us.
func (s *Store) TouchUpstreamKey(ctx context.Context, keyID, ip, lastError string) {
	_, _ = s.DB.Exec(ctx, `UPDATE upstream_keys SET last_used_at=now(),last_used_ip=nullif($2,'')::inet,last_error=$3 WHERE key_id=$1`, keyID, ip, lastError)
}

// RecordUpstreamHostLink remembers which 魔方 hostid maps to which service.
func (s *Store) RecordUpstreamHostLink(ctx context.Context, keyPublicID, servicePublicID, upstreamHostID, upstreamUserID, domain string) error {
	_, err := s.DB.Exec(ctx, `INSERT INTO upstream_host_links(upstream_key_id,service_public_id,upstream_host_id,upstream_user_id,upstream_domain)
SELECT k.id,$2,$3,$4,$5 FROM upstream_keys k WHERE k.public_id=$1
ON CONFLICT (upstream_key_id,service_public_id) DO UPDATE SET
upstream_host_id=excluded.upstream_host_id,
upstream_user_id=excluded.upstream_user_id,
upstream_domain=excluded.upstream_domain,
updated_at=now()`, keyPublicID, servicePublicID, upstreamHostID, upstreamUserID, domain)
	return err
}

// UpstreamHostLink is one 魔方 host to ShitIDC service association.
type UpstreamHostLink struct {
	ServicePublicID string    `json:"service_id"`
	UpstreamHostID  string    `json:"upstream_host_id"`
	UpstreamUserID  string    `json:"upstream_user_id"`
	UpstreamDomain  string    `json:"upstream_domain"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ListUpstreamHostLinks returns the links recorded for one key.
func (s *Store) ListUpstreamHostLinks(ctx context.Context, keyPublicID string, limit int) ([]UpstreamHostLink, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.DB.Query(ctx, `SELECT l.service_public_id::text,l.upstream_host_id,l.upstream_user_id,l.upstream_domain,l.updated_at
FROM upstream_host_links l JOIN upstream_keys k ON k.id=l.upstream_key_id
WHERE k.public_id=$1 ORDER BY l.id DESC LIMIT $2`, keyPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UpstreamHostLink{}
	for rows.Next() {
		var v UpstreamHostLink
		if err := rows.Scan(&v.ServicePublicID, &v.UpstreamHostID, &v.UpstreamUserID, &v.UpstreamDomain, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ServiceInstance is a service plus the instance payload the provider returned,
// used by the 魔方 module to read back generated credentials.
type ServiceInstance struct {
	Service model.Service  `json:"service"`
	Data    map[string]any `json:"data"`
}

// GetServiceInstance resolves one service with its stored instance payload.
func (s *Store) GetServiceInstance(ctx context.Context, servicePublicID string) (ServiceInstance, error) {
	var svc model.Service
	var payload []byte
	// billing_cycle / price / currency live on the order item, not the service.
	err := s.DB.QueryRow(ctx, `SELECT s.public_id::text,s.status,s.provider_type,coalesce(s.provider_ref,''),coalesce(oi.product_name,''),coalesce(oi.billing_cycle,'monthly'),coalesce(oi.unit_price_cents,0),coalesce(o.currency,'CNY'),s.expires_at,s.created_at,s.provider_payload
FROM services s LEFT JOIN order_items oi ON oi.id=s.order_item_id LEFT JOIN orders o ON o.id=s.order_id
WHERE s.public_id=$1`, servicePublicID).
		Scan(&svc.PublicID, &svc.Status, &svc.ProviderType, &svc.ProviderRef, &svc.ProductName, &svc.BillingCycle, &svc.PriceCents, &svc.Currency, &svc.ExpiresAt, &svc.CreatedAt, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServiceInstance{}, ErrNotFound
	}
	if err != nil {
		return ServiceInstance{}, err
	}
	data := map[string]any{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &data)
	}
	return ServiceInstance{Service: svc, Data: data}, nil
}
