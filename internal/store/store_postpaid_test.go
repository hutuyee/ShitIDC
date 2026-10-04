package store

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// 后付费的风险全在「授信额度」上，所以测试重点是额度的一致性。

// enableCredit 给用户开通后付费。
func enableCredit(t *testing.T, s *Store, userID int64, limitCents int64, days int) {
	t.Helper()
	var publicID string
	if err := s.DB.QueryRow(context.Background(), `SELECT public_id::text FROM users WHERE id=$1`, userID).Scan(&publicID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCredit(context.Background(), publicID, true, limitCents, days, "test grant", 0); err != nil {
		t.Fatalf("set credit: %v", err)
	}
}

func TestPostpaidDefaultsToDisabled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	productID := seedProduct(t, s, 10000)
	// 注册就拿到赊账能力是不可接受的：默认必须关闭。
	acct, err := s.CreditAccount(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Enabled {
		t.Fatal("a fresh account must not have postpaid enabled")
	}
	if acct.LimitCents != 0 || acct.AvailableCents != 0 {
		t.Fatalf("fresh account has credit: %+v", acct)
	}
	// 没有授信时下后付费订单必须被拒。
	_, err = s.CreateOrderPostpaid(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{}, "", true)
	if !errors.Is(err, ErrPostpaidNotEnabled) {
		t.Fatalf("postpaid order without credit = %v, want ErrPostpaidNotEnabled", err)
	}
}

func TestPostpaidOrderRecordsMethodAndCreditWindow(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	productID := seedProduct(t, s, 10000)
	enableCredit(t, s, userID, 100_00, 15)

	order, err := s.CreateOrderPostpaid(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{}, "", true)
	if err != nil {
		t.Fatalf("postpaid order: %v", err)
	}
	// 订单必须记成后付费，否则统计与催收都找不到它。
	var payMethod string
	if err := s.DB.QueryRow(ctx, `SELECT pay_method FROM orders WHERE public_id=$1`, order.PublicID).Scan(&payMethod); err != nil {
		t.Fatal(err)
	}
	if payMethod != "postpaid" {
		t.Fatalf("pay_method = %q, want postpaid", payMethod)
	}
	// 发票到期日必须是账期（15 天）而不是默认的 24 小时。
	var dueAt time.Time
	if err := s.DB.QueryRow(ctx, `SELECT i.due_at FROM invoices i JOIN orders o ON o.id=i.order_id WHERE o.public_id=$1`, order.PublicID).Scan(&dueAt); err != nil {
		t.Fatal(err)
	}
	if time.Until(dueAt) < 13*24*time.Hour {
		t.Fatalf("invoice due in %v, want roughly the 15-day credit window", time.Until(dueAt))
	}
	// 额度必须被占用。
	acct, err := s.CreditAccount(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.UsedCents != 10000 {
		t.Fatalf("used = %d, want 10000 occupied by the unpaid order", acct.UsedCents)
	}
	if acct.AvailableCents != 100_00-10000 {
		t.Fatalf("available = %d, want %d", acct.AvailableCents, 100_00-10000)
	}
}

func TestPostpaidRespectsCreditLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	productID := seedProduct(t, s, 10000)
	// 额度刚好够一单。
	enableCredit(t, s, userID, 10000, 30)
	if _, err := s.CreateOrderPostpaid(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{}, "", true); err != nil {
		t.Fatalf("first order: %v", err)
	}
	// 第二单会超出额度，必须被拒。
	_, err := s.CreateOrderPostpaid(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{}, "", true)
	if !errors.Is(err, ErrCreditLimitExceeded) {
		t.Fatalf("second order = %v, want ErrCreditLimitExceeded", err)
	}
	// 付清第一单后额度释放，又能下单了。
	var orderPublic string
	if err := s.DB.QueryRow(ctx, `SELECT public_id::text FROM orders WHERE user_id=$1 AND pay_method='postpaid'`, userID).Scan(&orderPublic); err != nil {
		t.Fatal(err)
	}
	// 用户没钱，用管理员调整余额来模拟还款资金。
	var walletUser int64
	_ = walletUser
	if _, err := s.DB.Exec(ctx, `UPDATE wallet_accounts SET balance_cents=100000 WHERE user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PayOrderWithWallet(ctx, userID, orderPublic, ""); err != nil {
		t.Fatalf("pay: %v", err)
	}
	acct, err := s.CreditAccount(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.UsedCents != 0 {
		t.Fatalf("used = %d, want 0 after paying the order", acct.UsedCents)
	}
	if _, err := s.CreateOrderPostpaid(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{}, "", true); err != nil {
		t.Fatalf("order after repayment: %v", err)
	}
}

// 并发下后付费订单不能把额度用超——这是整个功能最关键的一致性点。
func TestPostpaidConcurrentOrdersCannotExceedLimit(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	productID := seedProduct(t, s, 10000)
	// 额度只够 3 单。
	enableCredit(t, s, userID, 30000, 30)

	const attempts = 10
	var wg sync.WaitGroup
	var mu sync.Mutex
	succeeded := 0
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.CreateOrderPostpaid(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{}, "", true)
			if err == nil {
				mu.Lock()
				succeeded++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if succeeded != 3 {
		t.Fatalf("%d of %d concurrent orders succeeded, want exactly 3 (the credit line)", succeeded, attempts)
	}
	// 占用不得超过额度。
	acct, err := s.CreditAccount(ctx, userID)
	if err != nil {
		t.Fatal(err)
	}
	if acct.UsedCents > acct.LimitCents {
		t.Fatalf("used %d exceeds the limit %d", acct.UsedCents, acct.LimitCents)
	}
	if acct.UsedCents != 30000 {
		t.Fatalf("used = %d, want exactly 30000", acct.UsedCents)
	}
}

func TestCreditSuspendRequiresSettledDebt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	productID := seedProduct(t, s, 10000)
	enableCredit(t, s, userID, 100_00, 30)
	if _, err := s.CreateOrderPostpaid(ctx, userID, productID, "monthly", 1, "", OrderConfigInput{}, "", true); err != nil {
		t.Fatal(err)
	}
	var publicID string
	if err := s.DB.QueryRow(ctx, `SELECT public_id::text FROM users WHERE id=$1`, userID).Scan(&publicID); err != nil {
		t.Fatal(err)
	}
	// 有欠款时不允许停用授信：那等于把风险直接留在账上又失去追索依据。
	err := s.SetCredit(ctx, publicID, false, 0, 30, "revoke", 0)
	if err == nil {
		t.Fatal("disabling credit with unpaid postpaid orders must fail")
	}
	if !strings.Contains(err.Error(), "未付") {
		t.Fatalf("error %q should explain the outstanding debt", err.Error())
	}
}

func TestCreditEventsAreRecorded(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	enableCredit(t, s, userID, 5000, 30)
	var events []string
	rows, err := s.DB.Query(ctx, `SELECT event FROM credit_events WHERE user_id=$1 ORDER BY id`, userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			t.Fatal(err)
		}
		events = append(events, e)
	}
	if len(events) != 1 || events[0] != "enable" {
		t.Fatalf("events = %v, want a single enable event", events)
	}
	// 额度调整必须留痕，否则出坏账无法复盘。
	var publicID string
	if err := s.DB.QueryRow(ctx, `SELECT public_id::text FROM users WHERE id=$1`, userID).Scan(&publicID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCredit(ctx, publicID, true, 20000, 45, "raise limit", 0); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.DB.QueryRow(ctx, `SELECT count(*) FROM credit_events WHERE user_id=$1`, userID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("credit events = %d, want 2", count)
	}
	var after int64
	if err := s.DB.QueryRow(ctx, `SELECT after_limit_cents FROM credit_events WHERE user_id=$1 ORDER BY id DESC LIMIT 1`, userID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 20000 {
		t.Fatalf("after_limit = %d, want 20000", after)
	}
}
