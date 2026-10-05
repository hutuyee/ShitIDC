package provider

import (
	"context"
	"errors"
	"time"
)

type CreateRequest struct {
	RequestID  string         `json:"request_id"`
	ProductRef string         `json:"product_ref"`
	UserID     int64          `json:"user_id"`
	Options    map[string]any `json:"options,omitempty"`
}

type Instance struct {
	ID   string         `json:"id"`
	Data map[string]any `json:"data,omitempty"`
}

// ChangePackageRequest describes an in-place plan change on an existing
// instance (magic cube server modules call this _ChangePackage).
type ChangePackageRequest struct {
	InstanceID     string
	ProductRef     string
	BillingCycle   string
	PriceCents     int64
	SelectionsJSON map[string]any
}

// ErrChangePackageUnsupported is returned by providers that cannot change an
// existing instance's plan. Callers treat it as "bookkeeping only, no upstream
// change" rather than a hard failure.
var ErrChangePackageUnsupported = errors.New("provider does not support changing an existing instance's package")

// RenewRequest 描述一次续费：上游需要新到期时间（魔方 server 模块的
// _Renew 就把 $params['nextduedate'] 同步给上游），调用方没有到期时间时
// 传零值，由上游按自己的周期处理。
type RenewRequest struct {
	InstanceID string
	ExpiresAt  time.Time
}

type Provider interface {
	TestConnection(context.Context) error
	Create(context.Context, CreateRequest) (*Instance, error)
	Suspend(context.Context, string) error
	Unsuspend(context.Context, string) error
	Terminate(context.Context, string) error
	Renew(context.Context, RenewRequest) error
	// ChangePackage applies an upgrade/downgrade to an existing instance.
	ChangePackage(context.Context, ChangePackageRequest) error
}

type Manual struct{}

func (Manual) TestConnection(context.Context) error { return nil }
func (Manual) Create(_ context.Context, req CreateRequest) (*Instance, error) {
	return &Instance{ID: "manual:" + req.RequestID, Data: map[string]any{"mode": "manual"}}, nil
}
func (Manual) Suspend(context.Context, string) error                     { return nil }
func (Manual) Unsuspend(context.Context, string) error                   { return nil }
func (Manual) Terminate(context.Context, string) error                   { return nil }
func (Manual) Renew(context.Context, RenewRequest) error                 { return nil }
func (Manual) ChangePackage(context.Context, ChangePackageRequest) error { return nil }
