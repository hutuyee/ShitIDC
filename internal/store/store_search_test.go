package store

import (
	"context"
	"testing"
)

// 后台「不再要求手抄 UUID」依赖两个能力：统一搜索与用户详情聚合。
// 两者都直接影响管理员能否找对人，所以都要有测试。

func TestAdminSearchFindsUserByEmailUIDAndUUID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 5000)
	var publicID, email string
	if err := s.DB.QueryRow(ctx, `SELECT public_id::text,email FROM users WHERE id=$1`, userID).Scan(&publicID, &email); err != nil {
		t.Fatal(err)
	}

	// 1) 按邮箱片段搜索。
	hits, err := s.AdminSearch(ctx, email[:6], "user", 20)
	if err != nil {
		t.Fatalf("search by email: %v", err)
	}
	if !containsUser(hits, publicID) {
		t.Fatalf("searching %q did not find the user: %+v", email[:6], hits)
	}
	// 2) 按 UID 搜索（纯数字）。
	hits, err = s.AdminSearch(ctx, itoa(userID), "user", 20)
	if err != nil {
		t.Fatalf("search by uid: %v", err)
	}
	if !containsUser(hits, publicID) {
		t.Fatalf("searching by UID %d did not find the user", userID)
	}
	// 3) 按完整 UUID 搜索。
	hits, err = s.AdminSearch(ctx, publicID, "user", 20)
	if err != nil {
		t.Fatalf("search by uuid: %v", err)
	}
	if !containsUser(hits, publicID) {
		t.Fatalf("searching by UUID did not find the user")
	}
	// 搜不到时返回空列表而不是报错。
	hits, err = s.AdminSearch(ctx, "no-such-user-xyz", "user", 20)
	if err != nil {
		t.Fatalf("empty search errored: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("expected no hits, got %+v", hits)
	}
}

func TestAdminSearchFindsProducts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	productID := seedProduct(t, s, 12345)

	hits, err := s.AdminSearch(ctx, "test-product", "product", 20)
	if err != nil {
		t.Fatalf("search product: %v", err)
	}
	found := false
	for _, h := range hits {
		if h.Kind == "product" && h.ID == productID {
			found = true
			// 结果里要带上价格，管理员才能确认选对了商品。
			if h.Sub == "" {
				t.Fatalf("product hit should carry a price subtitle: %+v", h)
			}
		}
	}
	if !found {
		t.Fatalf("product not found by name: %+v", hits)
	}

	// 按 UUID 也能找到。
	hits, err = s.AdminSearch(ctx, productID, "product", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("product not found by UUID")
	}
}

func TestAdminSearchKindFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// 造一个邮箱里含关键字、商品名里也含关键字的场景，验证 kind 过滤真的生效。
	userID := seedUser(t, s, 0)
	if _, err := s.DB.Exec(ctx, `UPDATE users SET email='zzz-probe@test.local' WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateProduct(ctx, "zzz-probe-product", "", "manual", "", "", "monthly", "CNY", 100); err != nil {
		t.Fatal(err)
	}
	all, err := s.AdminSearch(ctx, "zzz-probe", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, h := range all {
		kinds[h.Kind] = true
	}
	if !kinds["user"] || !kinds["product"] {
		t.Fatalf("unfiltered search should return both kinds, got %+v", kinds)
	}
	onlyUser, err := s.AdminSearch(ctx, "zzz-probe", "user", 20)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range onlyUser {
		if h.Kind != "user" {
			t.Fatalf("kind=user returned a %s hit", h.Kind)
		}
	}
}

func TestUserDetailAdmin(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// 余额要够付 10000 分的商品，否则付款会因余额不足失败。
	userID := seedUser(t, s, 20000)
	productID := seedProduct(t, s, 10000)
	order, err := s.CreateOrder(ctx, userID, productID, "monthly", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PayOrderWithWallet(ctx, userID, order.PublicID, ""); err != nil {
		t.Fatalf("pay: %v", err)
	}
	var publicID string
	if err := s.DB.QueryRow(ctx, `SELECT public_id::text FROM users WHERE id=$1`, userID).Scan(&publicID); err != nil {
		t.Fatal(err)
	}

	// 按 UUID 取详情。
	d, err := s.UserDetailAdmin(ctx, publicID)
	if err != nil {
		t.Fatalf("detail by uuid: %v", err)
	}
	if d.UID != userID {
		t.Fatalf("uid = %d, want %d", d.UID, userID)
	}
	if len(d.Wallets) == 0 {
		t.Fatal("detail must include the wallet balances")
	}
	if d.OrderCount < 1 {
		t.Fatalf("order count = %d, want at least 1", d.OrderCount)
	}
	if d.ServiceCount < 1 {
		t.Fatalf("service count = %d, want at least 1 (the paid order provisions a service)", d.ServiceCount)
	}
	if len(d.Orders) == 0 || len(d.Services) == 0 {
		t.Fatalf("detail must inline recent orders and services: orders=%d services=%d", len(d.Orders), len(d.Services))
	}
	// 累计已付要算上刚付的那笔。
	if d.PaidTotalCents < 10000 {
		t.Fatalf("paid total = %d, want at least 10000", d.PaidTotalCents)
	}

	// 纯数字 UID 也能取到同一个用户。
	byUID, err := s.UserDetailAdmin(ctx, itoa(userID))
	if err != nil {
		t.Fatalf("detail by uid: %v", err)
	}
	if byUID.PublicID != d.PublicID {
		t.Fatalf("detail by UID returned a different user: %s vs %s", byUID.PublicID, d.PublicID)
	}

	if _, err := s.UserDetailAdmin(ctx, "00000000-0000-0000-0000-000000000000"); err != ErrNotFound {
		t.Fatalf("missing user = %v, want ErrNotFound", err)
	}
}

// ---- 小工具 ----

func containsUser(hits []SearchHit, publicID string) bool {
	for _, h := range hits {
		if h.Kind == "user" && h.ID == publicID {
			return true
		}
	}
	return false
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte(48 + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = 45
	}
	return string(buf[i:])
}
