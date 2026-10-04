// Package webhook delivers core events to third-party HTTP endpoints
// (第二十一阶段 Webhook). Deliveries are HMAC-signed and timestamped, sent
// through the SSRF-safe HTTP client and executed on the queue so a slow
// receiver never blocks business code.
package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/queue"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// Envelope is the JSON body POSTed to receivers.
type Envelope struct {
	ID         string         `json:"id"`
	Event      string         `json:"event"`
	OccurredAt time.Time      `json:"occurred_at"`
	RequestID  string         `json:"request_id,omitempty"`
	Data       map[string]any `json:"data"`
}

// Fanout subscribes to the event bus and persists one pending delivery per
// matching webhook, then enqueues the actual HTTP call. Best-effort: a
// webhook outage must never affect the emitting flow.
func Fanout(st *store.Store, q *queue.Client, decrypt func(string) (string, error)) func(context.Context, events.Event) {
	return func(ctx context.Context, e events.Event) {
		webhooks, _, err := st.MatchedWebhookSecrets(ctx, e.Name)
		if err != nil {
			log.Printf("webhook fanout %s: match: %v", e.Name, err)
			return
		}
		for _, wh := range webhooks {
			deliveryID, err := st.CreateWebhookDelivery(ctx, wh.ID, e.Name, map[string]any{
				"id": uuid.NewString(), "event": e.Name, "occurred_at": e.OccurredAt, "request_id": e.RequestID, "data": e.Data,
			})
			if err != nil {
				log.Printf("webhook fanout %s: persist delivery: %v", e.Name, err)
				continue
			}
			if q == nil {
				continue
			}
			if err := q.WebhookDeliver(deliveryID); err != nil {
				log.Printf("webhook fanout %s: enqueue delivery %d: %v", e.Name, deliveryID, err)
			}
		}
	}
}

// SignPayload produces the value of the X-ShitIDC-Signature header:
// "t=<unix>,v1=<hex hmac-sha256(t + '.' + body)>" — the receiver verifies
// with its copy of the secret and a fresh timestamp to block replays.
func SignPayload(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + strconv.FormatInt(timestamp, 10) + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

// Deliver posts one envelope and returns the HTTP status (0 on transport
// error). Only 2xx counts as delivered; asynq retries the rest.
func Deliver(ctx context.Context, client *http.Client, url, secret string, env Envelope) (int, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return 0, err
	}
	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "ShitIDC-Webhook/1.0")
	req.Header.Set("X-ShitIDC-Event", env.Event)
	req.Header.Set("X-ShitIDC-Delivery", env.ID)
	req.Header.Set("X-ShitIDC-Signature", SignPayload(secret, ts, body))
	if env.RequestID != "" {
		req.Header.Set("X-Request-ID", env.RequestID)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("receiver answered %s", resp.Status)
	}
	return resp.StatusCode, nil
}

// ProcessDelivery is the worker-side handler: load the pending delivery and
// webhook secret, decrypt, sign and send, then record the outcome.
func ProcessDelivery(ctx context.Context, st *store.Store, masterKey []byte, deliveryID int64) error {
	d, url, secretEncrypted, ok, err := st.GetWebhookDeliveryForWork(ctx, deliveryID)
	if err != nil {
		return err
	}
	if !ok {
		return nil // already delivered or webhook deleted
	}
	if len(masterKey) == 0 {
		_ = st.CompleteWebhookDelivery(ctx, deliveryID, false, 0, "MASTER_KEY_BASE64 not configured")
		return errors.New("master key not configured")
	}
	secret, err := security.Decrypt(masterKey, secretEncrypted)
	if err != nil {
		_ = st.CompleteWebhookDelivery(ctx, deliveryID, false, 0, "secret decrypt failed")
		return err
	}
	// The stored payload already has the envelope shape; rehydrate it.
	var env Envelope
	if b, merr := json.Marshal(d.Payload); merr == nil {
		_ = json.Unmarshal(b, &env)
	}
	if env.Event == "" {
		env.Event = d.Event
	}
	if env.Data == nil {
		env.Data = map[string]any{}
	}
	client := security.SafeHTTPClient(false, 15*time.Second)
	code, err := Deliver(ctx, client, url, secret, env)
	if err != nil {
		_ = st.CompleteWebhookDelivery(ctx, deliveryID, false, code, err.Error())
		return fmt.Errorf("webhook delivery %s to %s: %w", d.PublicID, url, err)
	}
	return st.CompleteWebhookDelivery(ctx, deliveryID, true, code, "")
}
