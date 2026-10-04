package store

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// ---- outbound webhooks ----

func (s *Store) CreateWebhook(ctx context.Context, name, url, secretEncrypted string, events []string) (model.Webhook, error) {
	var v model.Webhook
	var eventsOut []string
	err := s.DB.QueryRow(ctx, `INSERT INTO webhooks(name,url,secret_encrypted,events) VALUES($1,$2,$3,$4)
RETURNING id,public_id::text,name,url,events,active,last_delivery_at,last_delivery_ok,created_at,updated_at`,
		name, url, secretEncrypted, events).Scan(&v.ID, &v.PublicID, &v.Name, &v.URL, &eventsOut, &v.Active, &v.LastDeliveryAt, &v.LastDeliveryOK, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return model.Webhook{}, err
	}
	if eventsOut == nil {
		eventsOut = []string{}
	}
	v.Events = eventsOut
	return v, nil
}

func (s *Store) UpdateWebhook(ctx context.Context, publicID, name, url, secretEncrypted string, events []string, active bool) (model.Webhook, error) {
	var v model.Webhook
	var eventsOut []string
	err := s.DB.QueryRow(ctx, `UPDATE webhooks SET name=$2,url=$3,
secret_encrypted=CASE WHEN NULLIF($4,'') IS NULL THEN secret_encrypted ELSE $4 END,events=$5,active=$6,updated_at=now()
WHERE public_id=$1 RETURNING id,public_id::text,name,url,events,active,last_delivery_at,last_delivery_ok,created_at,updated_at`,
		publicID, name, url, secretEncrypted, events, active).Scan(&v.ID, &v.PublicID, &v.Name, &v.URL, &eventsOut, &v.Active, &v.LastDeliveryAt, &v.LastDeliveryOK, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.Webhook{}, ErrNotFound
	}
	if err != nil {
		return model.Webhook{}, err
	}
	if eventsOut == nil {
		eventsOut = []string{}
	}
	v.Events = eventsOut
	return v, nil
}

func (s *Store) DeleteWebhook(ctx context.Context, publicID string) error {
	cmd, err := s.DB.Exec(ctx, `DELETE FROM webhooks WHERE public_id=$1`, publicID)
	if err != nil {
		return err
	}
	if cmd.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) ListWebhooks(ctx context.Context) ([]model.Webhook, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,public_id::text,name,url,events,active,last_delivery_at,last_delivery_ok,created_at,updated_at FROM webhooks ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Webhook{}
	for rows.Next() {
		var v model.Webhook
		if err := rows.Scan(&v.ID, &v.PublicID, &v.Name, &v.URL, &v.Events, &v.Active, &v.LastDeliveryAt, &v.LastDeliveryOK, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		if v.Events == nil {
			v.Events = []string{}
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// MatchedWebhookSecrets returns active webhooks subscribed to the event.
// An empty subscription list means "all events".
func (s *Store) MatchedWebhookSecrets(ctx context.Context, event string) ([]model.Webhook, []string, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,public_id::text,name,url,events,active,last_delivery_at,last_delivery_ok,created_at,updated_at,secret_encrypted
FROM webhooks WHERE active=true AND (cardinality(events)=0 OR $1=ANY(events))`, event)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var out []model.Webhook
	var secrets []string
	for rows.Next() {
		var v model.Webhook
		var secret string
		if err := rows.Scan(&v.ID, &v.PublicID, &v.Name, &v.URL, &v.Events, &v.Active, &v.LastDeliveryAt, &v.LastDeliveryOK, &v.CreatedAt, &v.UpdatedAt, &secret); err != nil {
			return nil, nil, err
		}
		if v.Events == nil {
			v.Events = []string{}
		}
		out = append(out, v)
		secrets = append(secrets, secret)
	}
	return out, secrets, rows.Err()
}

// CreateWebhookDelivery records a pending delivery and returns its row id.
func (s *Store) CreateWebhookDelivery(ctx context.Context, webhookID int64, event string, payload map[string]any) (int64, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var id int64
	err = s.DB.QueryRow(ctx, `INSERT INTO webhook_deliveries(webhook_id,event,payload) VALUES($1,$2,$3::jsonb) RETURNING id`, webhookID, event, b).Scan(&id)
	return id, err
}

// GetWebhookDeliveryForWork loads a pending delivery plus its webhook URL and
// decrypted-ready secret for the worker.
func (s *Store) GetWebhookDeliveryForWork(ctx context.Context, deliveryID int64) (model.WebhookDelivery, string, string, bool, error) {
	var d model.WebhookDelivery
	var url, secret string
	var payload []byte
	err := s.DB.QueryRow(ctx, `SELECT d.public_id::text,d.webhook_id,d.event,d.status,d.attempts,d.response_code,d.last_error,d.payload,d.created_at,d.delivered_at,w.url,w.secret_encrypted
FROM webhook_deliveries d JOIN webhooks w ON w.id=d.webhook_id WHERE d.id=$1`, deliveryID).Scan(&d.PublicID, &d.WebhookID, &d.Event, &d.Status, &d.Attempts, &d.ResponseCode, &d.LastError, &payload, &d.CreatedAt, &d.DeliveredAt, &url, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.WebhookDelivery{}, "", "", false, nil
	}
	if err != nil {
		return model.WebhookDelivery{}, "", "", false, err
	}
	if d.Status != "pending" {
		return model.WebhookDelivery{}, "", "", false, nil
	}
	var raw any
	_ = json.Unmarshal(payload, &raw)
	d.Payload = raw
	return d, url, secret, true, nil
}

func (s *Store) CompleteWebhookDelivery(ctx context.Context, deliveryID int64, ok bool, code int, errText string) error {
	status := "failed"
	if ok {
		status = "delivered"
	}
	_, err := s.DB.Exec(ctx, `UPDATE webhook_deliveries SET status=$2,attempts=attempts+1,response_code=$3,last_error=$4,delivered_at=CASE WHEN $2='delivered' THEN now() ELSE delivered_at END WHERE id=$1`, deliveryID, status, code, errText)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(ctx, `UPDATE webhooks SET last_delivery_at=now(),last_delivery_ok=$2,updated_at=now() WHERE id=(SELECT webhook_id FROM webhook_deliveries WHERE id=$1)`, deliveryID, ok)
	return err
}

func (s *Store) ListWebhookDeliveries(ctx context.Context, webhookPublicID string, limit int) ([]model.WebhookDelivery, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.Query(ctx, `SELECT d.public_id::text,d.event,d.status,d.attempts,d.response_code,d.last_error,d.payload,d.created_at,d.delivered_at
FROM webhook_deliveries d JOIN webhooks w ON w.id=d.webhook_id WHERE w.public_id=$1 ORDER BY d.id DESC LIMIT $2`, webhookPublicID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.WebhookDelivery{}
	for rows.Next() {
		var v model.WebhookDelivery
		var payload []byte
		if err := rows.Scan(&v.PublicID, &v.Event, &v.Status, &v.Attempts, &v.ResponseCode, &v.LastError, &payload, &v.CreatedAt, &v.DeliveredAt); err != nil {
			return nil, err
		}
		var raw any
		_ = json.Unmarshal(payload, &raw)
		v.Payload = raw
		out = append(out, v)
	}
	return out, rows.Err()
}
