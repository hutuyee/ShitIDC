// Package events is the core hook bus (第十一阶段 Hook 系统).
//
// Core code only emits events at well-defined business moments; it never
// calls extension or webhook code directly. Subscribers (webhook fan-out,
// future extension runtime) register once at process start and receive
// events after the originating transaction has committed.
package events

import (
	"context"
	"log"
	"sync"
	"time"
)

// Canonical event names. Keep additive: never rename, only add.
const (
	UserRegistered     = "user.registered"
	UserLogin          = "user.login"
	UserPasswordReset  = "user.password_reset"
	OrderCreated       = "order.created"
	OrderPaid          = "order.paid"
	OrderCancelled     = "order.cancelled"
	OrderRefunded      = "order.refunded"
	InvoicePaid        = "invoice.paid"
	WalletRecharged    = "wallet.recharged"
	WalletAdjusted     = "wallet.adjusted"
	PaymentFailed      = "payment.failed"
	ServiceCreated     = "service.created"
	ServiceFailed      = "service.failed"
	ServiceUpdated     = "service.updated"
	ServiceRenewed     = "service.renewed"
	ServiceSuspended   = "service.suspended"
	ServiceUnsuspended = "service.unsuspended"
	ServiceTerminated  = "service.terminated"
	TicketCreated      = "ticket.created"
	TicketReplied      = "ticket.replied"
)

// Event is one committed business moment.
type Event struct {
	Name       string         `json:"name"`
	OccurredAt time.Time      `json:"occurred_at"`
	RequestID  string         `json:"request_id,omitempty"`
	Data       map[string]any `json:"data"`
}

type handlerFunc func(ctx context.Context, e Event)

// Bus is a synchronous in-process pub/sub. Handlers must be fast and
// non-blocking; anything slow belongs behind the queue (see webhook.Fanout).
type Bus struct {
	mu       sync.RWMutex
	handlers []handlerFunc
}

func New() *Bus { return &Bus{} }

// Subscribe registers a handler. Call once at startup, not per request.
func (b *Bus) Subscribe(fn handlerFunc) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers = append(b.handlers, fn)
}

// Emit notifies every handler. A panicking or failing handler is logged and
// skipped; emitting must never break the business flow that triggered it.
func (b *Bus) Emit(ctx context.Context, name string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	e := Event{Name: name, OccurredAt: time.Now().UTC(), Data: data}
	if rid, ok := ctx.Value(requestIDKey{}).(string); ok {
		e.RequestID = rid
	}
	b.mu.RLock()
	handlers := append([]handlerFunc(nil), b.handlers...)
	b.mu.RUnlock()
	for _, fn := range handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					// A subscriber bug must never take down the request path.
					log.Printf("event handler panic on %s: %v", name, r)
				}
			}()
			fn(ctx, e)
		}()
	}
}

type requestIDKey struct{}

// WithRequestID tags a context so emitted events carry the request id.
func WithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey{}, requestID)
}
