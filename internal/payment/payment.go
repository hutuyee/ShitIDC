// Package payment is the PaymentProvider abstraction (第七阶段).
//
// Checkout and notify flows talk to the Gateway interface, never to a
// concrete channel. Adding a channel means registering one more
// implementation — the API, wallet and order code stay untouched. Gateways
// that support server-side money movement also implement Refunder (自动退款).
package payment

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/hutuyee/ShitIDC/internal/epay"
)

// Prepared is one checkout request resolved by the store: who pays what.
type Prepared struct {
	OutTradeNo  string
	Subject     string
	AmountCents int64
	Currency    string // ISO code of the payable amount (CNY / USD / ...)
	PayType     string // channel inside the gateway (alipay / wxpay / ...)
	NotifyURL   string
	ReturnURL   string
	SiteName    string
}

// ProviderConfig is the decrypted gateway credential set of one
// payment_providers row. Secret is gateway-specific: epay uses the raw key,
// JSON-based gateways (stripe / alipay) keep their key set as a JSON object.
type ProviderConfig struct {
	Method     string
	GatewayURL string
	MerchantID string
	Secret     string
}

// NotifyInput carries everything a callback needs. RawBody is required by
// signature schemes that hash the exact bytes (Stripe), while merged
// query+form values serve classic form-based gateways (epay / alipay).
type NotifyInput struct {
	Method   string
	Query    url.Values
	PostForm url.Values
	RawBody  []byte
	Header   http.Header
}

// Values merges query and form values; form values win on duplicates.
func (n NotifyInput) Values() url.Values {
	out := url.Values{}
	for k, vs := range n.Query {
		for _, v := range vs {
			out.Add(k, v)
		}
	}
	for k, vs := range n.PostForm {
		for _, v := range vs {
			out.Set(k, v)
		}
	}
	return out
}

// NotifyResult is a verified gateway callback.
type NotifyResult struct {
	OK          bool   // signature + status + amount all valid
	TradeNo     string // gateway transaction id
	AmountCents int64
	Err         error
}

// Gateway is one online payment channel implementation.
type Gateway interface {
	Method() string
	PayURL(ctx context.Context, cfg ProviderConfig, p Prepared) (string, error)
	VerifyNotify(cfg ProviderConfig, input NotifyInput) NotifyResult
}

// RefundRequest describes a gateway-side refund (自动退款).
type RefundRequest struct {
	OutTradeNo  string // merchant order/payment number
	TradeNo     string // gateway transaction id
	AmountCents int64
	RefundID    string // our refund public id, used as the idempotency key
	Reason      string
}

// Refunder is implemented by gateways that can move money back to the payer.
type Refunder interface {
	Refund(ctx context.Context, cfg ProviderConfig, r RefundRequest) (gatewayRefundID string, err error)
}

// ErrTradeStatus reports a callback whose signature was valid but whose
// trade status does not mean paid.
var ErrTradeStatus = errors.New("trade_status is not success")

// ErrGatewayAPI wraps HTTP failures toward a gateway API (checkout/refund).
type ErrGatewayAPI struct{ Detail string }

func (e ErrGatewayAPI) Error() string { return "gateway api error: " + e.Detail }

var (
	regMu sync.RWMutex
	reg   = map[string]Gateway{}
)

// Register installs a gateway implementation. Call at package init time.
func Register(g Gateway) {
	regMu.Lock()
	defer regMu.Unlock()
	reg[strings.ToLower(g.Method())] = g
}

// Get returns the gateway for a method ("" defaults to epay).
func Get(method string) (Gateway, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	if method == "" {
		method = "epay"
	}
	g, ok := reg[strings.ToLower(method)]
	return g, ok
}

// Methods lists registered method names, sorted.
func Methods() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for m := range reg {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// EpayGateway adapts the 易支付 protocol (MD5 signed redirect checkout).
type EpayGateway struct{}

func (EpayGateway) Method() string { return "epay" }

func (EpayGateway) PayURL(_ context.Context, cfg ProviderConfig, p Prepared) (string, error) {
	c := epay.Config{GatewayURL: cfg.GatewayURL, PID: cfg.MerchantID, Key: cfg.Secret}
	if err := c.Valid(); err != nil {
		return "", err
	}
	return c.PayURL(epay.SubmitParams{
		PayType:    p.PayType,
		OutTradeNo: p.OutTradeNo,
		Name:       p.Subject,
		MoneyCents: p.AmountCents,
		NotifyURL:  p.NotifyURL,
		ReturnURL:  p.ReturnURL,
		SiteName:   p.SiteName,
	})
}

func (EpayGateway) VerifyNotify(cfg ProviderConfig, input NotifyInput) NotifyResult {
	c := epay.Config{GatewayURL: cfg.GatewayURL, PID: cfg.MerchantID, Key: cfg.Secret}
	values := input.Values()
	if err := c.VerifyNotify(values); err != nil {
		return NotifyResult{OK: false, Err: err}
	}
	if values.Get("trade_status") != epay.StatusSuccess {
		return NotifyResult{OK: false, Err: ErrTradeStatus}
	}
	cents, err := epay.MoneyToCents(values.Get("money"))
	if err != nil {
		return NotifyResult{OK: false, Err: err}
	}
	return NotifyResult{OK: true, TradeNo: values.Get("trade_no"), AmountCents: cents}
}

func init() {
	Register(EpayGateway{})
}
