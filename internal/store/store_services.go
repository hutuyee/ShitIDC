package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- service lifecycle (suspend / unsuspend / terminate) ----

// ServiceRef carries everything a worker needs to act on a service.
type ServiceRef struct {
	PublicID     string
	UserID       int64
	ProviderID   int64
	ProviderType string
	ProviderRef  string
}

// ClaimServiceForTransition atomically moves a service into a transitional
// status (suspending/terminating/unsuspending→provisioning is not used) and
// returns the reference for the provider call. ok=false when the service is
// not in one of the allowed source states.
func (s *Store) ClaimServiceForTransition(ctx context.Context, publicID, toStatus string, from ...string) (ServiceRef, string, bool, error) {
	var ref ServiceRef
	var prev string
	var providerID int64
	var providerType string
	var providerRef *string
	err := s.DB.QueryRow(ctx, `UPDATE services SET status=$2,
provider_payload=provider_payload||jsonb_build_object('transition_from',status,'transition_to',$2,'transition_at',now()),updated_at=now()
WHERE public_id=$1 AND status=ANY($3)
RETURNING public_id::text,user_id,coalesce(provider_id,0),provider_type,provider_ref,status`,
		publicID, toStatus, from).Scan(&ref.PublicID, &ref.UserID, &providerID, &providerType, &providerRef, &prev)
	if errors.Is(err, pgx.ErrNoRows) {
		return ServiceRef{}, "", false, nil
	}
	if err != nil {
		return ServiceRef{}, "", false, err
	}
	ref.ProviderID = providerID
	ref.ProviderType = providerType
	if providerRef != nil {
		ref.ProviderRef = *providerRef
	}
	return ref, prev, true, nil
}

// FinalizeServiceTransition completes or reverts a transition claimed with
// ClaimServiceForTransition. On success the service lands in finalStatus
// (with the matching timestamp); on failure it returns to the status it had
// before the claim and the error is recorded in provider_payload.
func (s *Store) FinalizeServiceTransition(ctx context.Context, publicID, finalStatus string, ok bool, errText string) error {
	if ok {
		ts := ""
		switch finalStatus {
		case "suspended":
			ts = ", suspended_at=now()"
		case "terminated":
			ts = ", terminated_at=now()"
		}
		query := `UPDATE services SET status=$2,
provider_payload=(provider_payload - 'transition_from' - 'transition_to')||jsonb_build_object('last_transition_ok',true,'last_transition_error',''),updated_at=now()` + ts + `
WHERE public_id=$1`
		_, err := s.DB.Exec(ctx, query, publicID, finalStatus)
		return err
	}
	// Revert to the pre-claim status and keep the failure reason.
	tag, err := s.DB.Exec(ctx, `UPDATE services SET status=coalesce(provider_payload->>'transition_from',status),
provider_payload=(provider_payload - 'transition_from' - 'transition_to')||jsonb_build_object('last_transition_ok',false,'last_transition_error',$2),updated_at=now()
WHERE public_id=$1 AND provider_payload ? 'transition_from'`, publicID, errText)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		_, err = s.DB.Exec(ctx, `UPDATE services SET provider_payload=provider_payload||jsonb_build_object('last_transition_ok',false,'last_transition_error',$2),updated_at=now() WHERE public_id=$1`, publicID, errText)
		return err
	}
	return nil
}

// ExpiredActiveServices returns active services past their expiry for the
// scheduler's auto-suspend job.
func (s *Store) ExpiredActiveServices(ctx context.Context, limit int) ([]ServiceRef, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,user_id,coalesce(provider_id,0),provider_type,coalesce(provider_ref,'')
FROM services WHERE status='active' AND expires_at IS NOT NULL AND expires_at<now() ORDER BY expires_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanServiceRefs(rows)
}

// StaleSuspendedServices returns services suspended more than `days` ago,
// used for optional auto-termination.
func (s *Store) StaleSuspendedServices(ctx context.Context, days, limit int) ([]ServiceRef, error) {
	if days <= 0 {
		return nil, nil
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,user_id,coalesce(provider_id,0),provider_type,coalesce(provider_ref,'')
FROM services WHERE status='suspended' AND suspended_at IS NOT NULL AND suspended_at<now()-make_interval(days=>$1) ORDER BY suspended_at LIMIT $2`, days, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanServiceRefs(rows)
}

func scanServiceRefs(rows pgx.Rows) ([]ServiceRef, error) {
	out := []ServiceRef{}
	for rows.Next() {
		var v ServiceRef
		if err := rows.Scan(&v.PublicID, &v.UserID, &v.ProviderID, &v.ProviderType, &v.ProviderRef); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ClaimServiceForRenew loads an active or suspended service for a provider
// renew call without changing its status (the expiry was already extended
// when the renewal order was paid).
func (s *Store) ClaimServiceForRenew(ctx context.Context, publicID string) (ServiceRef, bool, error) {
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,user_id,coalesce(provider_id,0),provider_type,coalesce(provider_ref,'')
FROM services WHERE public_id=$1 AND status IN ('active','suspended')`, publicID)
	if err != nil {
		return ServiceRef{}, false, err
	}
	defer rows.Close()
	refs, err := scanServiceRefs(rows)
	if err != nil || len(refs) == 0 {
		return ServiceRef{}, false, err
	}
	return refs[0], true, nil
}

// PatchServicePayload merges a patch into provider_payload.
func (s *Store) PatchServicePayload(ctx context.Context, publicID string, patch map[string]any) error {
	b, err := json.Marshal(patch)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE services SET provider_payload=provider_payload||$2::jsonb,updated_at=now() WHERE public_id=$1`, publicID, string(b))
	return err
}

// StuckService is a service that has sat in a transitional status too long
// (queue lost the task); Action is the queue task to re-enqueue.
type StuckService struct {
	ServiceRef
	TransitionTo string
}

// StuckTransitionalServices finds services stuck in provisioning/suspending/
// unsuspending/terminating for more than `minutes` so the scheduler can
// re-enqueue them.
func (s *Store) StuckTransitionalServices(ctx context.Context, minutes, limit int) ([]StuckService, error) {
	if minutes <= 0 {
		minutes = 10
	}
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,user_id,coalesce(provider_id,0),provider_type,coalesce(provider_ref,''),
coalesce(provider_payload->>'transition_to',CASE status WHEN 'provisioning' THEN 'provision' ELSE '' END)
FROM services WHERE status IN ('provisioning','suspending','unsuspending','terminating') AND updated_at<now()-make_interval(mins=>$1)
ORDER BY updated_at LIMIT $2`, minutes, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StuckService{}
	for rows.Next() {
		var v StuckService
		var transition string
		if err := rows.Scan(&v.PublicID, &v.UserID, &v.ProviderID, &v.ProviderType, &v.ProviderRef, &transition); err != nil {
			return nil, err
		}
		v.TransitionTo = transition
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetServiceOwner returns the owner user id of a service public id.
func (s *Store) GetServiceOwner(ctx context.Context, publicID string) (int64, error) {
	var userID int64
	err := s.DB.QueryRow(ctx, `SELECT user_id FROM services WHERE public_id=$1`, publicID).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	return userID, err
}

// FindPaidServiceByPaymentKey resolves the services created by a wallet
// payment made under the given idempotency key, making compat create calls
// retry-safe: a repeated Idempotency-Key returns the already-provisioned
// service instead of double-charging.
func (s *Store) FindPaidServiceByPaymentKey(ctx context.Context, idempotencyKey string) ([]string, error) {
	rows, err := s.DB.Query(ctx, `SELECT sv.public_id::text FROM wallet_transactions wt
JOIN orders o ON o.public_id = wt.reference_id
JOIN services sv ON sv.order_id = o.id
WHERE wt.idempotency_key=$1 AND wt.type='debit' AND wt.reference_type='order'`, idempotencyKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListServicesAdmin returns services for the admin console with owner email.
func (s *Store) ListServicesAdmin(ctx context.Context, status string, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.DB.Query(ctx, `SELECT s.public_id::text,s.status,s.provider_type,coalesce(s.provider_ref,''),p.name,u.id,u.email,
s.expires_at,s.suspended_at,s.terminated_at,s.created_at
FROM services s JOIN products p ON p.id=s.product_id JOIN users u ON u.id=s.user_id
WHERE ($1='' OR s.status=$1) ORDER BY s.created_at DESC LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var publicID, statusV, providerType, providerRef, product, email string
		var uid int64
		var expires, suspended, terminated *time.Time
		var created time.Time
		if err := rows.Scan(&publicID, &statusV, &providerType, &providerRef, &product, &uid, &email, &expires, &suspended, &terminated, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": publicID, "status": statusV, "provider_type": providerType, "provider_ref": providerRef, "product_name": product, "user_uid": uid, "user_email": email, "expires_at": expires, "suspended_at": suspended, "terminated_at": terminated, "created_at": created})
	}
	return out, rows.Err()
}

// CountProvisioningServices 统计尚未开通完成的服务（等待入队 + 开通中），
// 对应魔方待办事项 widget/ToDo 的「开通中产品数量」。
func (s *Store) CountProvisioningServices(ctx context.Context) (int64, error) {
	var n int64
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM services WHERE status IN ('pending','provisioning')`).Scan(&n)
	return n, err
}
