package store

import (
	"context"
	"errors"
	"testing"
)

// 接口分组的挑选逻辑是纯函数，先把它钉死，再测数据库侧。

func member(id int64, current, max, weight int, active bool) GroupMember {
	return GroupMember{ProviderID: id, Current: current, MaxServices: max, Weight: weight, Active: active}
}

func TestPickProviderLeastLoaded(t *testing.T) {
	members := []GroupMember{
		member(1, 5, 0, 0, true),
		member(2, 2, 0, 0, true),
		member(3, 9, 0, 0, true),
	}
	if got := pickProvider(StrategyLeastLoaded, members); got != 2 {
		t.Fatalf("least_loaded picked %d, want the least busy provider 2", got)
	}
}

func TestPickProviderSkipsFullAndInactive(t *testing.T) {
	members := []GroupMember{
		member(1, 3, 3, 100, true),  // 满了
		member(2, 0, 0, 100, false), // 停用
		member(3, 1, 5, 0, true),    // 唯一可用
	}
	if got := pickProvider(StrategyLeastLoaded, members); got != 3 {
		t.Fatalf("picked %d, want the only provider with room (3)", got)
	}
}

func TestPickProviderAllFullReturnsNone(t *testing.T) {
	members := []GroupMember{
		member(1, 2, 2, 0, true),
		member(2, 1, 1, 0, true),
	}
	if got := pickProvider(StrategyLeastLoaded, members); got != -1 {
		t.Fatalf("picked %d, want -1 when every provider is full", got)
	}
	if got := pickProvider(StrategyLeastLoaded, nil); got != -1 {
		t.Fatalf("picked %d, want -1 for an empty group", got)
	}
}

func TestPickProviderPrefersUnlimitedCapacity(t *testing.T) {
	// 不限容量的接口比「快满了」的接口更适合接新单，即使后者负载更低。
	members := []GroupMember{
		member(1, 4, 5, 0, true), // 只剩 1 个位置
		member(2, 5, 0, 0, true), // 不限容量
	}
	if got := pickProvider(StrategyLeastLoaded, members); got != 2 {
		t.Fatalf("picked %d, want the unlimited provider 2", got)
	}
}

func TestPickProviderWeightBreaksTies(t *testing.T) {
	members := []GroupMember{
		member(1, 0, 0, 0, true),
		member(2, 0, 0, 10, true), // 权重更高
	}
	if got := pickProvider(StrategyLeastLoaded, members); got != 2 {
		t.Fatalf("picked %d, want the higher-weight provider 2", got)
	}
}

func TestPickProviderFillFirst(t *testing.T) {
	// 凑满一个再下一个：先继续填已经有负载的那个。
	members := []GroupMember{
		member(1, 3, 10, 0, true),
		member(2, 0, 10, 0, true),
	}
	if got := pickProvider(StrategyFillFirst, members); got != 1 {
		t.Fatalf("fill_first picked %d, want to keep filling provider 1", got)
	}
	// 装了东西的那个满了，就切到下一个。
	members2 := []GroupMember{
		member(1, 10, 10, 0, true),
		member(2, 0, 10, 0, true),
	}
	if got := pickProvider(StrategyFillFirst, members2); got != 2 {
		t.Fatalf("fill_first picked %d, want to move on to provider 2", got)
	}
	// 全空时挑第一个（按权重/ID 排序后的第一个）。
	members3 := []GroupMember{member(1, 0, 0, 0, true), member(2, 0, 0, 0, true)}
	if got := pickProvider(StrategyFillFirst, members3); got != 1 {
		t.Fatalf("fill_first on an empty group picked %d, want 1", got)
	}
}

func TestPickProviderIsDeterministic(t *testing.T) {
	members := []GroupMember{
		member(3, 1, 0, 0, true),
		member(1, 1, 0, 0, true),
		member(2, 1, 0, 0, true),
	}
	// 负载与权重都一样时按 ID 升序，结果必须稳定。
	first := pickProvider(StrategyLeastLoaded, members)
	for i := 0; i < 20; i++ {
		if got := pickProvider(StrategyLeastLoaded, members); got != first {
			t.Fatalf("pick is not deterministic: %d vs %d", got, first)
		}
	}
	if first != 1 {
		t.Fatalf("tie-break picked %d, want the lowest id 1", first)
	}
}

func TestNormalizeStrategy(t *testing.T) {
	for in, want := range map[string]string{
		"":             StrategyLeastLoaded,
		"  ":           StrategyLeastLoaded,
		"LEAST_LOADED": StrategyLeastLoaded,
		"fill_first":   StrategyFillFirst,
		"Round_Robin":  StrategyRoundRobin,
	} {
		got, err := normalizeStrategy(in)
		if err != nil {
			t.Fatalf("normalizeStrategy(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("normalizeStrategy(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := normalizeStrategy("random"); err == nil {
		t.Fatal("an unknown strategy must be rejected")
	}
}

// ---- 数据库侧：分组 CRUD 与按策略解析 ----

// seedProvider 建一个接口，返回公开 ID。
func seedProvider(t *testing.T, s *Store, name string) string {
	t.Helper()
	// CreateProvider 要求 name 与 base_url 都非空；分组测试不真的连上游，
	// 所以给一个占位地址即可。
	pv, err := s.CreateProvider(context.Background(), name, "manual", "https://provider.invalid", "", "", map[string]any{})
	if err != nil {
		t.Fatalf("create provider %s: %v", name, err)
	}
	return pv.PublicID
}

func TestProviderGroupLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	g, err := s.CreateProviderGroup(ctx, "香港节点", "least_loaded", "香港的机器")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	if g.Strategy != StrategyLeastLoaded {
		t.Fatalf("strategy = %q, want the default least_loaded", g.Strategy)
	}
	// 同名分组必须被拒绝。
	if _, err := s.CreateProviderGroup(ctx, "香港节点", "", ""); err == nil {
		t.Fatal("duplicate group name must be rejected")
	}

	pa := seedProvider(t, s, "前端A")
	pb := seedProvider(t, s, "前端B")
	if err := s.AddProviderToGroup(ctx, g.PublicID, pa, 1, 0); err != nil {
		t.Fatalf("add A: %v", err)
	}
	if err := s.AddProviderToGroup(ctx, g.PublicID, pb, 0, 0); err != nil {
		t.Fatalf("add B: %v", err)
	}
	members, err := s.ListGroupMembers(ctx, g.PublicID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("group has %d members, want 2", len(members))
	}
	// A 容量 1、B 不限容量：least_loaded 应选不限容量的 B。
	id, err := s.ResolveProviderForGroup(ctx, g.PublicID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	var wantID int64
	for _, m := range members {
		if m.PublicID == pb {
			wantID = m.ProviderID
		}
	}
	if id != wantID {
		t.Fatalf("resolved provider %d, want the unlimited one %d", id, wantID)
	}

	// 移出 B 之后，只剩容量为 1 的 A，仍然可用。
	if err := s.RemoveProviderFromGroup(ctx, g.PublicID, pb); err != nil {
		t.Fatalf("remove B: %v", err)
	}
	if _, err := s.ResolveProviderForGroup(ctx, g.PublicID); err != nil {
		t.Fatalf("resolve after removal: %v", err)
	}

	// 把 A 也移走，组就空了，解析必须报 NotFound 而不是挑到随机接口。
	if err := s.RemoveProviderFromGroup(ctx, g.PublicID, pa); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveProviderForGroup(ctx, g.PublicID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("empty group resolve = %v, want ErrNotFound", err)
	}
}

func TestProviderGroupRespectsCapacity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 1_000_00)
	productID := seedProduct(t, s, 1000)

	g, err := s.CreateProviderGroup(ctx, "容量组", "least_loaded", "")
	if err != nil {
		t.Fatal(err)
	}
	pa := seedProvider(t, s, "容量A")
	pb := seedProvider(t, s, "容量B")
	// 各限 1 台。
	if err := s.AddProviderToGroup(ctx, g.PublicID, pa, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProviderToGroup(ctx, g.PublicID, pb, 1, 0); err != nil {
		t.Fatal(err)
	}

	// 开两台服务，分别挂在两个接口上。
	for i := 0; i < 2; i++ {
		order, err := s.CreateOrder(ctx, userID, productID, "monthly", 1, "")
		if err != nil {
			t.Fatalf("order %d: %v", i, err)
		}
		if _, err := s.PayOrderWithWallet(ctx, userID, order.PublicID, ""); err != nil {
			t.Fatalf("pay %d: %v", i, err)
		}
	}
	// 把两个接口都挂上服务，容量就满了。
	var ids []int64
	members, _ := s.ListGroupMembers(ctx, g.PublicID)
	for _, m := range members {
		ids = append(ids, m.ProviderID)
	}
	if len(ids) != 2 {
		t.Fatalf("expected 2 members, got %d", len(ids))
	}
	if _, err := s.DB.Exec(ctx, `UPDATE services SET provider_id=$1`, ids[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE services SET provider_id=$1 WHERE id=(SELECT max(id) FROM services)`, ids[1]); err != nil {
		t.Fatal(err)
	}
	// 两个接口都满了：解析必须报 NotFound，让上层提示扩容。
	if _, err := s.ResolveProviderForGroup(ctx, g.PublicID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("full group resolve = %v, want ErrNotFound", err)
	}
	// 终止其中一个服务后容量释放，又能开了。
	if _, err := s.DB.Exec(ctx, `UPDATE services SET status='terminated' WHERE id=(SELECT max(id) FROM services)`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveProviderForGroup(ctx, g.PublicID); err != nil {
		t.Fatalf("resolve after freeing capacity: %v", err)
	}
}

func TestProviderGroupInactiveIsNotResolvable(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	g, err := s.CreateProviderGroup(ctx, "停用组", "", "")
	if err != nil {
		t.Fatal(err)
	}
	pa := seedProvider(t, s, "停用组前端")
	if err := s.AddProviderToGroup(ctx, g.PublicID, pa, 0, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE provider_groups SET active=FALSE WHERE public_id=$1`, g.PublicID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ResolveProviderForGroup(ctx, g.PublicID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("inactive group resolve = %v, want ErrNotFound", err)
	}
}

// ---- 开通时按分组挑接口 ----

func TestClaimResolvesProviderFromGroup(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 1_000_00)
	productID := seedProduct(t, s, 1000)

	g, err := s.CreateProviderGroup(ctx, "开通分组", "least_loaded", "")
	if err != nil {
		t.Fatal(err)
	}
	pa := seedProvider(t, s, "开通A")
	pb := seedProvider(t, s, "开通B")
	if err := s.AddProviderToGroup(ctx, g.PublicID, pa, 1, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProviderToGroup(ctx, g.PublicID, pb, 1, 0); err != nil {
		t.Fatal(err)
	}
	// 商品绑定分组；provider_id 保持为空，逼出「按分组挑」这条路径。
	if _, err := s.DB.Exec(ctx, `UPDATE products SET provider_group_id=(SELECT id FROM provider_groups WHERE public_id=$1) WHERE public_id=$2`, g.PublicID, productID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE products SET provider_id=NULL WHERE public_id=$1`, productID); err != nil {
		t.Fatal(err)
	}

	// 下单（order_items 会带上商品的 provider_id，此刻是 NULL）。
	order, err := s.CreateOrder(ctx, userID, productID, "monthly", 1, "")
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	res, err := s.PayOrderWithWallet(ctx, userID, order.PublicID, "")
	if err != nil {
		t.Fatalf("pay: %v", err)
	}
	if len(res.ServiceIDs) != 1 {
		t.Fatalf("expected 1 service, got %d", len(res.ServiceIDs))
	}
	serviceID := res.ServiceIDs[0]

	_, _, providerID, _, _, claimed, err := s.ClaimServiceForProvisioning(ctx, serviceID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatal("service was not claimed")
	}
	if providerID == 0 {
		t.Fatal("claim did not resolve a provider from the group")
	}
	// 选中的接口必须落在服务行上，后续暂停/删除才找得到同一台机器。
	var storedProvider *int64
	var storedGroup *string
	if err := s.DB.QueryRow(ctx, `SELECT provider_id,(SELECT public_id::text FROM provider_groups WHERE id=services.provider_group_id) FROM services WHERE public_id=$1`, serviceID).Scan(&storedProvider, &storedGroup); err != nil {
		t.Fatal(err)
	}
	if storedProvider == nil || *storedProvider != providerID {
		t.Fatalf("service.provider_id = %v, want %d", storedProvider, providerID)
	}
	if storedGroup == nil || *storedGroup != g.PublicID {
		t.Fatalf("service.provider_group_id = %v, want %s", storedGroup, g.PublicID)
	}
}

func TestClaimFailsWhenGroupIsFull(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 1_000_00)
	productID := seedProduct(t, s, 1000)

	g, err := s.CreateProviderGroup(ctx, "满组", "least_loaded", "")
	if err != nil {
		t.Fatal(err)
	}
	pa := seedProvider(t, s, "满组A")
	// 容量 0 表示不限，用 1 才有意义；这里直接把接口停用，模拟「没有可用接口」。
	if err := s.AddProviderToGroup(ctx, g.PublicID, pa, 1, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE providers SET active=FALSE WHERE public_id=$1`, pa); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE products SET provider_group_id=(SELECT id FROM provider_groups WHERE public_id=$1),provider_id=NULL WHERE public_id=$2`, g.PublicID, productID); err != nil {
		t.Fatal(err)
	}

	order, err := s.CreateOrder(ctx, userID, productID, "monthly", 1, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := s.PayOrderWithWallet(ctx, userID, order.PublicID, "")
	if err != nil {
		t.Fatal(err)
	}
	serviceID := res.ServiceIDs[0]

	_, _, _, _, _, claimed, err := s.ClaimServiceForProvisioning(ctx, serviceID)
	if err == nil {
		t.Fatal("claiming with no usable provider in the group must fail")
	}
	if claimed {
		t.Fatal("service must not be reported as claimed")
	}
	// 服务必须回到 pending，等扩容后重试，而不是卡在 provisioning。
	var status string
	if err := s.DB.QueryRow(ctx, `SELECT status FROM services WHERE public_id=$1`, serviceID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("service status = %q, want pending so the scheduler retries", status)
	}
}
