package store

import (
	"context"
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hutuyee/ShitIDC/internal/security"
)

// Integration tests for the financial core (第二十三/六十阶段). They require
// a scratch PostgreSQL database; point TEST_DATABASE_URL at it to enable:
//
//	TEST_DATABASE_URL=postgres://shitidc:pw@localhost:5432/shitidc_test?sslmode=disable go test ./internal/store/
//
// Without the variable the tests skip, so `go test ./...` stays green on
// machines without a database.

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping integration test")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	// Simple protocol lets Exec run the multi-statement migration files.
	cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeSimpleProtocol
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	ctx := context.Background()
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("find migrations: %v", err)
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if _, err := pool.Exec(ctx, string(b)); err != nil {
			t.Fatalf("apply %s: %v", f, err)
		}
	}
	t.Cleanup(func() {
		// Wipe every table so tests stay independent of order.
		var tables []string
		rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname='public'`)
		if err != nil {
			return
		}
		for rows.Next() {
			var n string
			_ = rows.Scan(&n)
			tables = append(tables, `"`+n+`"`)
		}
		rows.Close()
		if len(tables) > 0 {
			_, _ = pool.Exec(ctx, "TRUNCATE "+strings.Join(tables, ",")+" RESTART IDENTITY CASCADE")
		}
	})
	return New(pool)
}

// seedUser creates a customer with a wallet balance and returns its id.
func seedUser(t *testing.T, s *Store, balanceCents int64) int64 {
	t.Helper()
	email := "u-" + uuid.NewString()[:8] + "@test.local"
	hash, err := security.HashPassword("password-123456")
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.CreateUser(context.Background(), email, hash, true)
	if err != nil {
		t.Fatal(err)
	}
	if balanceCents != 0 {
		if _, err := s.DB.Exec(context.Background(),
			`UPDATE wallet_accounts SET balance_cents=$2 WHERE user_id=$1`, u.ID, balanceCents); err != nil {
			t.Fatal(err)
		}
	}
	return u.ID
}

func seedProduct(t *testing.T, s *Store, priceCents int64) string {
	t.Helper()
	p, err := s.CreateProduct(context.Background(), "test-product", "", "manual", "", "", "monthly", "CNY", priceCents)
	if err != nil {
		t.Fatal(err)
	}
	return p.PublicID
}

// TestConcurrentWalletDeduction is the 第二十三阶段 §60 scenario: 100 元余额,
// 20 concurrent 10 元 debits must succeed exactly 10 times, never go negative
// and never double-spend.
func TestConcurrentWalletDeduction(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 10_000)
	productID := seedProduct(t, s, 1_000)

	const goroutines = 20
	type outcome struct {
		ok  bool
		err string
	}
	results := make([]outcome, goroutines)
	var wg sync.WaitGroup
	start := time.Now().Add(50 * time.Millisecond)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-time.After(time.Until(start))
			var res outcome
			// Serializable transactions can be aborted under contention;
			// a client retry is the documented pattern.
			for attempt := 0; attempt < 8; attempt++ {
				order, err := s.CreateOrder(ctx, userID, productID, "monthly", 1, "")
				if err != nil {
					res.err = err.Error()
					continue
				}
				_, err = s.PayOrderWithWallet(ctx, userID, order.PublicID, uuid.NewString())
				if err == nil {
					res.ok = true
					res.err = ""
					break
				}
				res.err = err.Error()
				if !strings.Contains(err.Error(), "SQLSTATE 40001") {
					break
				}
			}
			results[i] = res
		}(i)
	}
	wg.Wait()

	var successes int
	for i, r := range results {
		if r.ok {
			successes++
			continue
		}
		if r.err == "" {
			t.Fatalf("goroutine %d: neither succeeded nor recorded an error", i)
		}
	}
	if successes != 10 {
		t.Fatalf("expected exactly 10 successful debits, got %d", successes)
	}
	var balance int64
	if err := s.DB.QueryRow(ctx, `SELECT balance_cents FROM wallet_accounts WHERE user_id=$1`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 0 {
		t.Fatalf("balance must be exactly 0 after 10 debits of 10000, got %d", balance)
	}
	// Ledger must agree with the balance: sum of debits == amount spent.
	var spent int64
	if err := s.DB.QueryRow(ctx, `SELECT -sum(wt.amount_cents) FROM wallet_transactions wt
JOIN wallet_accounts wa ON wa.id=wt.account_id WHERE wa.user_id=$1 AND wt.amount_cents<0`, userID).Scan(&spent); err != nil {
		t.Fatal(err)
	}
	if spent != 10_000 {
		t.Fatalf("ledger debits %d != 10000 spent", spent)
	}
}

// TestOrderPriceRecalculatedBackendSide (第十三阶段 §13): the total must come
// from product_prices, not from anything a client could influence.
func TestOrderPriceRecalculatedBackendSide(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	productID := seedProduct(t, s, 555)

	o, err := s.CreateOrder(ctx, userID, productID, "monthly", 3, "")
	if err != nil {
		t.Fatal(err)
	}
	if o.TotalCents != 555*3 {
		t.Fatalf("order total %d != backend price 555*3", o.TotalCents)
	}
	var unit, subtotal int64
	if err := s.DB.QueryRow(ctx, `SELECT unit_price_cents,subtotal_cents FROM order_items WHERE order_id=(SELECT id FROM orders WHERE public_id=$1)`, o.PublicID).Scan(&unit, &subtotal); err != nil {
		t.Fatal(err)
	}
	if unit != 555 || subtotal != 1665 {
		t.Fatalf("order_items unit=%d subtotal=%d, want 555/1665", unit, subtotal)
	}
}

// TestDuplicatePaymentCallbackIdempotent (第十五阶段 §15): the same gateway
// notify must credit the wallet exactly once.
func TestDuplicatePaymentCallbackIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)

	pv, err := s.CreatePaymentProvider(ctx, "epay-test", "epay", "https://pay.example.com", "1001", "", map[string]any{"pay_types": []string{"alipay"}})
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := s.PrepareRecharge(ctx, userID, 100_00, pv.PublicID, "alipay")
	if err != nil {
		t.Fatal(err)
	}

	kind, uid, _, _, err := s.CompleteOnlinePayment(ctx, prepared.OutTradeNo, "GW-1", 100_00)
	if err != nil || kind != "recharge" || uid != userID {
		t.Fatalf("first complete: kind=%s uid=%d err=%v", kind, uid, err)
	}
	kind2, _, _, _, err := s.CompleteOnlinePayment(ctx, prepared.OutTradeNo, "GW-1", 100_00)
	if err != ErrAlreadyCompleted {
		t.Fatalf("second complete must be idempotent, got kind=%s err=%v", kind2, err)
	}
	var balance int64
	if err := s.DB.QueryRow(ctx, `SELECT balance_cents FROM wallet_accounts WHERE user_id=$1`, userID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 100_00 {
		t.Fatalf("balance %d != 10000 credited exactly once", balance)
	}
}

// TestRefundReversalKeepsLedger: a wallet-paid order refunded once restores
// the balance, marks the order refunded and cannot be refunded again.
func TestRefundReversalKeepsLedger(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 10_000)
	productID := seedProduct(t, s, 2_000)

	order, err := s.CreateOrder(ctx, userID, productID, "monthly", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PayOrderWithWallet(ctx, userID, order.PublicID, uuid.NewString()); err != nil {
		t.Fatal(err)
	}
	var afterPay int64
	_ = s.DB.QueryRow(ctx, `SELECT balance_cents FROM wallet_accounts WHERE user_id=$1`, userID).Scan(&afterPay)
	if afterPay != 8_000 {
		t.Fatalf("balance after pay %d != 8000", afterPay)
	}

	r, err := s.RefundOrder(ctx, 0, userID, order.PublicID, "test refund", "req-1")
	if err != nil {
		t.Fatalf("refund: %v", err)
	}
	if r.AmountCents != 2_000 {
		t.Fatalf("refund amount %d != 2000", r.AmountCents)
	}
	var balance int64
	_ = s.DB.QueryRow(ctx, `SELECT balance_cents FROM wallet_accounts WHERE user_id=$1`, userID).Scan(&balance)
	if balance != 10_000 {
		t.Fatalf("balance after refund %d != 10000", balance)
	}
	var orderStatus string
	_ = s.DB.QueryRow(ctx, `SELECT status FROM orders WHERE public_id=$1`, order.PublicID).Scan(&orderStatus)
	if orderStatus != "refunded" {
		t.Fatalf("order status %s != refunded", orderStatus)
	}
	// Double refund must be rejected (idempotency key + order status).
	if _, err := s.RefundOrder(ctx, 0, userID, order.PublicID, "again", "req-2"); err != ErrNotRefundable {
		t.Fatalf("second refund must fail with ErrNotRefundable, got %v", err)
	}
	// The original debit row must still exist untouched (冲正 not modification).
	var debitCount int
	_ = s.DB.QueryRow(ctx, `SELECT count(*) FROM wallet_transactions wt
JOIN wallet_accounts wa ON wa.id=wt.account_id WHERE wa.user_id=$1 AND wt.type='debit' AND wt.amount_cents=-2000`, userID).Scan(&debitCount)
	if debitCount != 1 {
		t.Fatalf("original debit row missing (count=%d) — ledger was modified", debitCount)
	}
}

// TestLegacyPasswordUpgrade covers the MagicCube migration path (第三十七阶段):
// legacy md5 verifies once and upgrades to Argon2id.
func TestLegacyPasswordUpgrade(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	email := "legacy-" + uuid.NewString()[:8] + "@test.local"
	hash, _ := security.HashPassword("irrelevant-password")
	u, err := s.CreateUser(ctx, email, hash, true)
	if err != nil {
		t.Fatal(err)
	}
	legacy := fmt.Sprintf("%x", md5.Sum([]byte("old-magiccube-password")))
	if _, err := s.DB.Exec(ctx, `UPDATE user_security SET legacy_password_hash=$2 WHERE user_id=$1`, u.ID, legacy); err != nil {
		t.Fatal(err)
	}

	creds, err := s.GetLoginCredentials(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if security.VerifyPassword(creds.PasswordHash, "old-magiccube-password") {
		t.Fatal("argon2 must not verify the legacy password")
	}
	if !security.VerifyLegacyPassword(creds.LegacyHash, "old-magiccube-password") {
		t.Fatal("legacy md5 verification failed")
	}
	if security.VerifyLegacyPassword(creds.LegacyHash, "wrong-password") {
		t.Fatal("legacy md5 verified a wrong password")
	}
	newHash, _ := security.HashPassword("old-magiccube-password")
	if err := s.UpgradeLegacyPassword(ctx, u.ID, newHash); err != nil {
		t.Fatal(err)
	}
	creds2, _ := s.GetLoginCredentials(ctx, email)
	if creds2.LegacyHash != "" {
		t.Fatal("legacy hash must be cleared after upgrade")
	}
	if !security.VerifyPassword(creds2.PasswordHash, "old-magiccube-password") {
		t.Fatal("upgraded argon2 hash must verify")
	}
}

// TestLoginFailureLockout persists the brute-force lockout across "restarts"
// (it lives in the database, not Redis).
func TestLoginFailureLockout(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	email := "lock-" + uuid.NewString()[:8] + "@test.local"
	hash, _ := security.HashPassword("correct-password-1")
	u, err := s.CreateUser(ctx, email, hash, true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := s.RegisterLoginFailure(ctx, email, 10, 15); err != nil {
			t.Fatal(err)
		}
	}
	creds, err := s.GetLoginCredentials(ctx, email)
	if err != nil {
		t.Fatal(err)
	}
	if creds.LockedUntil == nil || !creds.LockedUntil.After(time.Now()) {
		t.Fatal("account must be locked after 10 persisted failures")
	}
	if creds.FailedAttempts < 10 {
		t.Fatalf("failed attempts %d < 10", creds.FailedAttempts)
	}
	if err := s.ClearLoginFailures(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	creds2, _ := s.GetLoginCredentials(ctx, email)
	if creds2.LockedUntil != nil {
		t.Fatal("lock must be cleared")
	}
}
