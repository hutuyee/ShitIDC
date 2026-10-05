package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hutuyee/ShitIDC/internal/model"
)

// ---- payment providers (epay etc.) ----

func (s *Store) CreatePaymentProvider(ctx context.Context, name, method, gatewayURL, merchantID, secretEncrypted string, config map[string]any) (model.PaymentProvider, error) {
	cfg, err := json.Marshal(config)
	if err != nil {
		return model.PaymentProvider{}, err
	}
	var v model.PaymentProvider
	// $6::jsonb：string 才会被当作 JSON 文本，[]byte 会被 pgx 编码成 bytea 而报 22P02。
	err = s.DB.QueryRow(ctx, `INSERT INTO payment_providers(name,method,gateway_url,merchant_id,secret_encrypted,config)
VALUES($1,$2,$3,$4,$5,$6::jsonb) RETURNING id,public_id::text,name,method,gateway_url,merchant_id,config,active,secret_encrypted<>'',created_at,updated_at`,
		name, method, gatewayURL, merchantID, secretEncrypted, string(cfg)).Scan(&v.ID, &v.PublicID, &v.Name, &v.Method, &v.GatewayURL, &v.MerchantID, &v.Config, &v.Active, &v.HasSecret, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

func (s *Store) UpdatePaymentProvider(ctx context.Context, publicID, name, gatewayURL, merchantID, secretEncrypted string, config map[string]any, active bool) (model.PaymentProvider, error) {
	cfg, err := json.Marshal(config)
	if err != nil {
		return model.PaymentProvider{}, err
	}
	var v model.PaymentProvider
	err = s.DB.QueryRow(ctx, `UPDATE payment_providers SET name=$2,gateway_url=$3,merchant_id=$4,
secret_encrypted=CASE WHEN NULLIF($5,'') IS NULL THEN secret_encrypted ELSE $5 END,config=$6::jsonb,active=$7,updated_at=now()
WHERE public_id=$1 RETURNING id,public_id::text,name,method,gateway_url,merchant_id,config,active,secret_encrypted<>'',created_at,updated_at`,
		publicID, name, gatewayURL, merchantID, secretEncrypted, cfg, active).Scan(&v.ID, &v.PublicID, &v.Name, &v.Method, &v.GatewayURL, &v.MerchantID, &v.Config, &v.Active, &v.HasSecret, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.PaymentProvider{}, ErrNotFound
	}
	return v, err
}

func (s *Store) ListPaymentProviders(ctx context.Context) ([]model.PaymentProvider, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,public_id::text,name,method,gateway_url,merchant_id,config,active,secret_encrypted<>'',created_at,updated_at FROM payment_providers ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PaymentProvider{}
	for rows.Next() {
		var v model.PaymentProvider
		if err := rows.Scan(&v.ID, &v.PublicID, &v.Name, &v.Method, &v.GatewayURL, &v.MerchantID, &v.Config, &v.Active, &v.HasSecret, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s *Store) GetPaymentProviderCredentials(ctx context.Context, publicID string) (model.PaymentProvider, string, error) {
	var v model.PaymentProvider
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT id,public_id::text,name,method,gateway_url,merchant_id,config,active,secret_encrypted<>'',created_at,updated_at,secret_encrypted
FROM payment_providers WHERE public_id=$1`, publicID).Scan(&v.ID, &v.PublicID, &v.Name, &v.Method, &v.GatewayURL, &v.MerchantID, &v.Config, &v.Active, &v.HasSecret, &v.CreatedAt, &v.UpdatedAt, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.PaymentProvider{}, "", ErrNotFound
	}
	return v, secret, err
}

// ListEnabledPaymentMethods returns active providers for the checkout UI.
func (s *Store) ListEnabledPaymentMethods(ctx context.Context) ([]model.PaymentProvider, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,public_id::text,name,method,gateway_url,merchant_id,config,active,FALSE,created_at,updated_at FROM payment_providers WHERE active=true ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.PaymentProvider{}
	for rows.Next() {
		var v model.PaymentProvider
		if err := rows.Scan(&v.ID, &v.PublicID, &v.Name, &v.Method, &v.GatewayURL, &v.MerchantID, &v.Config, &v.Active, &v.HasSecret, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---- online order payment ----

type PreparedPayment struct {
	Provider    model.PaymentProvider
	Secret      string // encrypted secret; decrypt with master key
	OutTradeNo  string
	AmountCents int64
	Currency    string
	Subject     string
	PayType     string
}

// PrepareOrderOnlinePayment locks the caller's unpaid order and creates (or
// reuses) a pending payment row, returning everything needed to build the
// gateway redirect URL.
func (s *Store) PrepareOrderOnlinePayment(ctx context.Context, userID int64, orderPublicID, providerPublicID, payType string) (PreparedPayment, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return PreparedPayment{}, err
	}
	defer tx.Rollback(ctx)
	var orderID int64
	var total int64
	var currency, status string
	err = tx.QueryRow(ctx, `SELECT id,total_cents,currency,status FROM orders WHERE public_id=$1 AND user_id=$2 FOR UPDATE`, orderPublicID, userID).Scan(&orderID, &total, &currency, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return PreparedPayment{}, ErrNotFound
	}
	if err != nil {
		return PreparedPayment{}, err
	}
	if status != "unpaid" {
		return PreparedPayment{}, ErrInvalidState
	}
	if !strings.EqualFold(currency, "CNY") {
		return PreparedPayment{}, errors.New("在线支付暂只支持人民币计价订单")
	}
	var subject string
	if err := tx.QueryRow(ctx, `SELECT product_name FROM order_items WHERE order_id=$1 ORDER BY id LIMIT 1`, orderID).Scan(&subject); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return PreparedPayment{}, err
	}

	var pv model.PaymentProvider
	var secret string
	if err := tx.QueryRow(ctx, `SELECT id,public_id::text,name,method,gateway_url,merchant_id,config,active,FALSE,created_at,updated_at,secret_encrypted
FROM payment_providers WHERE public_id=$1 AND active=true`, providerPublicID).Scan(&pv.ID, &pv.PublicID, &pv.Name, &pv.Method, &pv.GatewayURL, &pv.MerchantID, &pv.Config, &pv.Active, &pv.HasSecret, &pv.CreatedAt, &pv.UpdatedAt, &secret); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PreparedPayment{}, ErrNotFound
		}
		return PreparedPayment{}, err
	}

	// 传 string 而不是 []byte：pgx 把 []byte 当 bytea 编码，写 jsonb 列会报 22P02。
	rawJSON, _ := json.Marshal(map[string]any{"kind": "order", "provider": pv.PublicID, "pay_type": payType})
	raw := string(rawJSON)
	outTradeNo := "O" + strings.ReplaceAll(orderPublicID, "-", "")
	var existing string
	err = tx.QueryRow(ctx, `SELECT transaction_id FROM payments WHERE order_id=$1 AND status='pending' FOR UPDATE`, orderID).Scan(&existing)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		_, err = tx.Exec(ctx, `INSERT INTO payments(user_id,order_id,method,transaction_id,amount_cents,currency,status,raw_payload)
VALUES($1,$2,$3,$4,$5,$6,'pending',$7::jsonb)`, userID, orderID, pv.Method, outTradeNo, total, currency, raw)
		if err != nil {
			return PreparedPayment{}, err
		}
	case err != nil:
		return PreparedPayment{}, err
	default:
		outTradeNo = existing
		_, err = tx.Exec(ctx, `UPDATE payments SET amount_cents=$2,currency=$3,raw_payload=$4::jsonb WHERE transaction_id=$1 AND status='pending'`, outTradeNo, total, currency, raw)
		if err != nil {
			return PreparedPayment{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return PreparedPayment{}, err
	}
	if payType == "" {
		payType = firstPayType(pv.Config)
	}
	return PreparedPayment{Provider: pv, Secret: secret, OutTradeNo: outTradeNo, AmountCents: total, Currency: currency, Subject: subject, PayType: payType}, nil
}

// EnsureOrderPendingPayment 锁定未支付订单并返回后台「确认收款」使用的
// pending 支付单：已有在线支付单就复用，用户从未发起在线支付时补建一条
// method=manual 的登记单。返回 out_trade_no、金额与下单用户 id。
func (s *Store) EnsureOrderPendingPayment(ctx context.Context, orderPublicID string) (string, int64, int64, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return "", 0, 0, err
	}
	defer tx.Rollback(ctx)
	var orderID, total, userID int64
	var currency, status string
	err = tx.QueryRow(ctx, `SELECT id,total_cents,currency,status,user_id FROM orders WHERE public_id=$1 FOR UPDATE`, orderPublicID).Scan(&orderID, &total, &currency, &status, &userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, 0, ErrNotFound
	}
	if err != nil {
		return "", 0, 0, err
	}
	if status != "unpaid" {
		return "", 0, 0, ErrInvalidState
	}
	outTradeNo := "O" + strings.ReplaceAll(orderPublicID, "-", "")
	amountCents := total
	err = tx.QueryRow(ctx, `SELECT transaction_id,amount_cents FROM payments WHERE order_id=$1 AND status='pending' FOR UPDATE`, orderID).Scan(&outTradeNo, &amountCents)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", 0, 0, err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		rawJSON, _ := json.Marshal(map[string]any{"kind": "order", "pay_type": "manual"})
		if _, err := tx.Exec(ctx, `INSERT INTO payments(user_id,order_id,method,transaction_id,amount_cents,currency,status,raw_payload)
VALUES($1,$2,'manual',$3,$4,$5,'pending',$6::jsonb)`, userID, orderID, outTradeNo, total, currency, string(rawJSON)); err != nil {
			return "", 0, 0, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", 0, 0, err
	}
	return outTradeNo, amountCents, userID, nil
}

func firstPayType(cfg map[string]any) string {
	if v, ok := cfg["pay_types"].([]any); ok && len(v) > 0 {
		if s, ok := v[0].(string); ok && s != "" {
			return s
		}
	}
	return "alipay"
}

// PrepareRecharge creates a pending recharge payment. Order id stays NULL;
// completion credits the wallet.
func (s *Store) PrepareRecharge(ctx context.Context, userID int64, amountCents int64, providerPublicID, payType string) (PreparedPayment, error) {
	if amountCents < 100 || amountCents > 100000000 {
		return PreparedPayment{}, errors.New("充值金额需在 1 元到 100 万元之间")
	}
	var pv model.PaymentProvider
	var secret string
	if err := s.DB.QueryRow(ctx, `SELECT id,public_id::text,name,method,gateway_url,merchant_id,config,active,FALSE,created_at,updated_at,secret_encrypted
FROM payment_providers WHERE public_id=$1 AND active=true`, providerPublicID).Scan(&pv.ID, &pv.PublicID, &pv.Name, &pv.Method, &pv.GatewayURL, &pv.MerchantID, &pv.Config, &pv.Active, &pv.HasSecret, &pv.CreatedAt, &pv.UpdatedAt, &secret); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return PreparedPayment{}, ErrNotFound
		}
		return PreparedPayment{}, err
	}
	if strings.EqualFold(pv.Method, "manual") {
		return PreparedPayment{}, errors.New("线下支付不支持余额充值，请选择在线支付渠道")
	}
	rawJSON, _ := json.Marshal(map[string]any{"kind": "recharge", "provider": pv.PublicID, "pay_type": payType})
	raw := string(rawJSON)
	outTradeNo := "R" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := s.DB.Exec(ctx, `INSERT INTO payments(user_id,method,transaction_id,amount_cents,currency,status,raw_payload)
VALUES($1,$2,$3,$4,'CNY','pending',$5::jsonb)`, userID, pv.Method, outTradeNo, amountCents, raw); err != nil {
		return PreparedPayment{}, err
	}
	if payType == "" {
		payType = firstPayType(pv.Config)
	}
	return PreparedPayment{Provider: pv, Secret: secret, OutTradeNo: outTradeNo, AmountCents: amountCents, Currency: "CNY", Subject: "余额充值", PayType: payType}, nil
}

// PaymentContext identifies which payment provider signed a callback.
type PaymentContext struct {
	ProviderPublicID string
	AmountCents      int64
	Status           string
	Kind             string
	UserID           int64
}

// GetPaymentContext loads a payment by gateway out_trade_no so the callback
// handler can verify the signature against the right provider.
func (s *Store) GetPaymentContext(ctx context.Context, outTradeNo string) (PaymentContext, error) {
	var v PaymentContext
	var providerPtr, kindPtr *string
	err := s.DB.QueryRow(ctx, `SELECT user_id,amount_cents,status,raw_payload->>'provider',raw_payload->>'kind' FROM payments WHERE transaction_id=$1`, outTradeNo).Scan(&v.UserID, &v.AmountCents, &v.Status, &providerPtr, &kindPtr)
	if errors.Is(err, pgx.ErrNoRows) {
		return PaymentContext{}, ErrNotFound
	}
	if err != nil {
		return PaymentContext{}, err
	}
	if providerPtr != nil {
		v.ProviderPublicID = *providerPtr
	}
	if kindPtr != nil && *kindPtr != "" {
		v.Kind = *kindPtr
	} else {
		v.Kind = "order"
	}
	return v, nil
}

// CompleteOnlinePayment marks a pending payment completed, then either
// finalises the order (invoice paid → order processing → services pending)
// or credits the wallet for a recharge. Safe to call repeatedly.
// Returns payment kind, user id and created service public ids.
func (s *Store) CompleteOnlinePayment(ctx context.Context, outTradeNo, gatewayTradeNo string, paidCents int64) (kind string, userID int64, serviceIDs []string, renewServiceID string, err error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return "", 0, nil, "", err
	}
	defer tx.Rollback(ctx)
	var paymentID int64
	var amount int64
	var status string
	var raw map[string]any
	var rawBytes []byte
	var orderID *int64
	err = tx.QueryRow(ctx, `SELECT id,amount_cents,status,order_id,raw_payload FROM payments WHERE transaction_id=$1 FOR UPDATE`, outTradeNo).Scan(&paymentID, &amount, &status, &orderID, &rawBytes)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", 0, nil, "", ErrNotFound
	}
	if err != nil {
		return "", 0, nil, "", err
	}
	_ = json.Unmarshal(rawBytes, &raw)
	if status == "completed" {
		kind, _ = raw["kind"].(string)
		return kind, 0, nil, "", ErrAlreadyCompleted
	}
	if paidCents != amount {
		_, _ = tx.Exec(ctx, `UPDATE payments SET status='failed',raw_payload=raw_payload||jsonb_build_object('mismatch_paid',$2::text) WHERE id=$1`, paymentID, paidCents)
		return "", 0, nil, "", ErrAmountMismatch
	}
	if _, err := tx.Exec(ctx, `UPDATE payments SET status='completed',raw_payload=raw_payload||jsonb_build_object('trade_no',$2::text,'completed_at',now()::text) WHERE id=$1`, paymentID, gatewayTradeNo); err != nil {
		return "", 0, nil, "", err
	}
	kind, _ = raw["kind"].(string)
	if kind == "" {
		kind = "order"
	}

	var renewID string
	if orderID != nil {
		var orderStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, *orderID).Scan(&orderStatus); err != nil {
			return "", 0, nil, "", err
		}
		if orderStatus != "unpaid" {
			// Order was already paid (wallet) or cancelled while the gateway
			// notify was in flight. Acknowledge so the gateway stops retrying.
			_, _ = tx.Exec(ctx, `UPDATE payments SET status=CASE WHEN $2::text IN ('cancelled') THEN 'failed' ELSE status END,raw_payload=raw_payload||jsonb_build_object('late_notify_order_status',$2::text) WHERE id=$1`, paymentID, orderStatus)
			if err := tx.Commit(ctx); err != nil {
				return "", 0, nil, "", err
			}
			return kind, 0, nil, "", ErrAlreadyCompleted
		}
		var invoiceID int64
		if err := tx.QueryRow(ctx, `UPDATE invoices SET status='paid',paid_at=now() WHERE order_id=$1 AND status='unpaid' RETURNING id`, *orderID).Scan(&invoiceID); err != nil {
			return "", 0, nil, "", err
		}
		if err := tx.QueryRow(ctx, `SELECT user_id FROM orders WHERE id=$1`, *orderID).Scan(&userID); err != nil {
			return "", 0, nil, "", err
		}
		var renewed bool
		renewID, renewed, err = s.ExtendServiceOnRenewalPayment(ctx, tx, *orderID)
		if err != nil {
			return "", 0, nil, "", err
		}
		if renewed {
			// Renewal order settled: expiry already extended, no new services.
			if err := tx.Commit(ctx); err != nil {
				return "", 0, nil, "", err
			}
			return kind, userID, nil, renewID, nil
		}
		if _, err := tx.Exec(ctx, `UPDATE orders SET status='processing',paid_at=now(),updated_at=now() WHERE id=$1`, *orderID); err != nil {
			return "", 0, nil, "", err
		}
		rows, err := tx.Query(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,status,provider_id,provider_type,provider_product_ref,expires_at)
SELECT $1,oi.order_id,oi.id,oi.product_id,'pending',oi.provider_id,oi.provider_type,oi.provider_product_ref,
now()+CASE oi.billing_cycle WHEN 'quarterly' THEN interval '3 months' WHEN 'semiannually' THEN interval '6 months' WHEN 'yearly' THEN interval '1 year' ELSE interval '1 month' END
FROM order_items oi WHERE oi.order_id=$2 RETURNING public_id::text`, userID, *orderID)
		if err != nil {
			return "", 0, nil, "", err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return "", 0, nil, "", err
			}
			serviceIDs = append(serviceIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return "", 0, nil, "", err
		}
	} else {
		var accountID, balance int64
		if err := tx.QueryRow(ctx, `SELECT id,balance_cents FROM wallet_accounts WHERE user_id=(SELECT user_id FROM payments WHERE id=$1) AND currency='CNY' FOR UPDATE`, paymentID).Scan(&accountID, &balance); err != nil {
			return "", 0, nil, "", err
		}
		newBalance := balance + amount
		if _, err := tx.Exec(ctx, `UPDATE wallet_accounts SET balance_cents=$2,updated_at=now() WHERE id=$1`, accountID, newBalance); err != nil {
			return "", 0, nil, "", err
		}
		var paymentPublic string
		if err := tx.QueryRow(ctx, `SELECT public_id::text FROM payments WHERE id=$1`, paymentID).Scan(&paymentPublic); err != nil {
			return "", 0, nil, "", err
		}
		if err := tx.QueryRow(ctx, `SELECT user_id FROM payments WHERE id=$1`, paymentID).Scan(&userID); err != nil {
			return "", 0, nil, "", err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO wallet_transactions(account_id,type,amount_cents,balance_before_cents,balance_after_cents,currency,reference_type,reference_id,description,idempotency_key)
VALUES($1,'credit',$2,$3,$4,'CNY','recharge',$5,'在线充值',$6)`,
			accountID, amount, balance, newBalance, paymentPublic, "epay:"+outTradeNo); err != nil {
			return "", 0, nil, "", err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return "", 0, nil, "", err
	}
	return kind, userID, serviceIDs, renewID, nil
}

// GetPaymentProviderByMethod returns the newest active provider of one
// gateway method together with its encrypted secret (gateway refunds).
func (s *Store) GetPaymentProviderByMethod(ctx context.Context, method string) (model.PaymentProvider, string, error) {
	var v model.PaymentProvider
	var secret string
	err := s.DB.QueryRow(ctx, `SELECT id,public_id::text,name,method,gateway_url,merchant_id,config,active,FALSE,created_at,updated_at,secret_encrypted
FROM payment_providers WHERE method=$1 AND active=true AND secret_encrypted<>'' ORDER BY id DESC LIMIT 1`, strings.ToLower(method)).Scan(&v.ID, &v.PublicID, &v.Name, &v.Method, &v.GatewayURL, &v.MerchantID, &v.Config, &v.Active, &v.HasSecret, &v.CreatedAt, &v.UpdatedAt, &secret)
	if errors.Is(err, pgx.ErrNoRows) {
		return model.PaymentProvider{}, "", ErrNotFound
	}
	return v, secret, err
}
