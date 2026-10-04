package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOAuthStateIsSingleUse(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.PutOAuthState(ctx, "st-1", "github", "/dashboard", 0); err != nil {
		t.Fatalf("put: %v", err)
	}
	row, err := s.ConsumeOAuthState(ctx, "st-1", 10*time.Minute)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if row.Provider != "github" || row.RedirectTo != "/dashboard" {
		t.Fatalf("state row = %+v", row)
	}
	// 第二次必须失败：state 是一次性的，重放会导致 CSRF。
	if _, err := s.ConsumeOAuthState(ctx, "st-1", 10*time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("replay = %v, want ErrNotFound", err)
	}
	// 过期 state 也必须被拒。
	if err := s.PutOAuthState(ctx, "st-old", "github", "/", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE oauth_states SET created_at=now()-interval '10 minutes' WHERE state='st-old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ConsumeOAuthState(ctx, "st-old", time.Minute); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired state = %v, want ErrNotFound", err)
	}
}

func TestFindUserByIdentity(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	userID := seedUser(t, s, 0)
	if err := s.BindOAuthIdentity(ctx, userID, IdentityInput{
		Provider: "github", Subject: "42", UnionID: "u-42", Nickname: "octocat",
	}); err != nil {
		t.Fatalf("bind: %v", err)
	}
	// 按 subject 命中。
	got, err := s.FindUserByIdentity(ctx, "github", "42", "")
	if err != nil || got != userID {
		t.Fatalf("by subject = %d/%v, want %d", got, err, userID)
	}
	// subject 变了但 union_id 相同：必须还是同一个人（微信多应用场景）。
	got2, err := s.FindUserByIdentity(ctx, "github", "different-openid", "u-42")
	if err != nil || got2 != userID {
		t.Fatalf("by union id = %d/%v, want %d", got2, err, userID)
	}
	// 都匹配不上就是没绑定。
	if _, err := s.FindUserByIdentity(ctx, "github", "nobody", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown identity = %v, want ErrNotFound", err)
	}
	// provider 必须区分：同 subject 在别的通道下不命中。
	if _, err := s.FindUserByIdentity(ctx, "wechat", "42", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-provider lookup = %v, want ErrNotFound", err)
	}
}

// 最关键的一条：第三方身份不能被静默改绑到别人身上。
func TestBindOAuthIdentityRefusesStealing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u1 := seedUser(t, s, 0)
	u2 := seedUser(t, s, 0)
	if err := s.BindOAuthIdentity(ctx, u1, IdentityInput{Provider: "github", Subject: "42", Nickname: "first"}); err != nil {
		t.Fatalf("first bind: %v", err)
	}
	err := s.BindOAuthIdentity(ctx, u2, IdentityInput{Provider: "github", Subject: "42", Nickname: "attacker"})
	if err == nil {
		t.Fatal("binding an identity that belongs to another user must fail")
	}
	if !strings.Contains(err.Error(), "已绑定到其它用户") {
		t.Fatalf("error %q should explain the conflict", err.Error())
	}
	// 原绑定必须完好无损，昵称不能被攻击者覆盖。
	ids, err := s.ListOAuthIdentities(ctx, u1)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0].Nickname != "first" {
		t.Fatalf("the original binding was modified: %+v", ids)
	}
	if ids2, _ := s.ListOAuthIdentities(ctx, u2); len(ids2) != 0 {
		t.Fatalf("the attacker gained a binding: %+v", ids2)
	}
	// 绑给同一个人是幂等的，并且允许刷新昵称。
	if err := s.BindOAuthIdentity(ctx, u1, IdentityInput{Provider: "github", Subject: "42", Nickname: "renamed"}); err != nil {
		t.Fatalf("rebinding to the same user should be idempotent: %v", err)
	}
	ids3, _ := s.ListOAuthIdentities(ctx, u1)
	if len(ids3) != 1 || ids3[0].Nickname != "renamed" {
		t.Fatalf("rebind did not refresh the profile: %+v", ids3)
	}
}

func TestUnbindRequiresAnotherLoginMethod(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// 建一个 OAuth 账号（没有可用密码）。
	userID, err := s.CreateOAuthUser(ctx, "oauth-only@test.local", "octocat", true)
	if err != nil {
		t.Fatalf("create oauth user: %v", err)
	}
	if has, err := s.UserHasPassword(ctx, userID); err != nil || has {
		t.Fatalf("an oauth-only account must report no password (has=%v err=%v)", has, err)
	}
	if err := s.BindOAuthIdentity(ctx, userID, IdentityInput{Provider: "github", Subject: "7"}); err != nil {
		t.Fatal(err)
	}
	// 唯一的登录方式：解绑必须被拒，否则账号永久失联。
	err = s.UnbindOAuthIdentity(ctx, userID, "github")
	if err == nil {
		t.Fatal("unbinding the only login method must fail")
	}
	if !strings.Contains(err.Error(), "唯一的登录方式") {
		t.Fatalf("error %q should explain why", err.Error())
	}
	// 多了第二个绑定之后就可以解绑其中一个。
	if err := s.BindOAuthIdentity(ctx, userID, IdentityInput{Provider: "wechat", Subject: "wx-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.UnbindOAuthIdentity(ctx, userID, "wechat"); err != nil {
		t.Fatalf("unbinding with another method left should work: %v", err)
	}
	// 设置了密码之后，唯一的第三方绑定也能解绑。
	hash, err := securityHash("password-123456")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(ctx, `UPDATE user_security SET password_hash=$2 WHERE user_id=$1`, userID, hash); err != nil {
		t.Fatal(err)
	}
	if err := s.UnbindOAuthIdentity(ctx, userID, "github"); err != nil {
		t.Fatalf("unbinding after setting a password should work: %v", err)
	}
	// 解绑一个不存在的绑定要报 NotFound。
	if err := s.UnbindOAuthIdentity(ctx, userID, "github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unbind missing = %v, want ErrNotFound", err)
	}
}

func TestCreateOAuthUserRefusesExistingEmail(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	// 已经有一个用该邮箱注册的账号。
	seedUser(t, s, 0)
	var email string
	if err := s.DB.QueryRow(ctx, `SELECT email FROM users LIMIT 1`).Scan(&email); err != nil {
		t.Fatal(err)
	}
	// 第三方登录拿到同一个邮箱：必须报错，交给「绑定已有账号」流程，
	// 绝不能凭空造一个新号或静默登进别人的号。
	_, err := s.CreateOAuthUser(ctx, email, "attacker", true)
	if err == nil {
		t.Fatal("auto-creating a user for an existing email must fail")
	}
	if !strings.Contains(err.Error(), "已被注册") {
		t.Fatalf("error %q should explain the conflict", err.Error())
	}
	// 没邮箱就必须拒绝自动注册。
	if _, err := s.CreateOAuthUser(ctx, "", "nick", false); err == nil {
		t.Fatal("auto-creating a user without an email must fail")
	}
}

func TestOAuthProviderLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	v, err := s.UpsertOAuthProvider(ctx, "GitHub", "github", map[string]string{"client_id": "cid"}, "enc-secret", true)
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if !v.HasSecret {
		t.Fatal("has_secret should be true after storing a secret")
	}
	// 再 upsert 一次但 secret 留空：密钥必须保留，不能被清掉。
	v2, err := s.UpsertOAuthProvider(ctx, "GitHub Renamed", "github", map[string]string{"client_id": "cid2"}, "", true)
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if !v2.HasSecret {
		t.Fatal("an empty secret on update must not wipe the stored one")
	}
	if v2.Name != "GitHub Renamed" {
		t.Fatalf("name = %q, want it updated", v2.Name)
	}
	// 只应有一条记录（provider 唯一）。
	all, err := s.ListOAuthProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("found %d providers, want 1", len(all))
	}
	// 停用后不出现在登录页，也不允许走登录流程。
	if err := s.SetOAuthProviderActive(ctx, v.PublicID, false); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveOAuthProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("a disabled provider is still offered: %+v", active)
	}
	if _, _, err := s.GetOAuthProviderSecret(ctx, "github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disabled provider secret lookup = %v, want ErrNotFound", err)
	}
	// 启用后又能取到。
	if err := s.SetOAuthProviderActive(ctx, v.PublicID, true); err != nil {
		t.Fatal(err)
	}
	_, secret, err := s.GetOAuthProviderSecret(ctx, "github")
	if err != nil {
		t.Fatalf("secret lookup: %v", err)
	}
	if secret != "enc-secret" {
		t.Fatalf("secret = %q", secret)
	}
	// 删除。
	if err := s.DeleteOAuthProvider(ctx, v.PublicID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteOAuthProvider(ctx, v.PublicID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete = %v, want ErrNotFound", err)
	}
}

func TestActiveOAuthProvidersHidesConfig(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if _, err := s.UpsertOAuthProvider(ctx, "GitHub", "github", map[string]string{
		"client_id": "cid", "client_secret_hint": "should-not-leak",
	}, "enc", true); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveOAuthProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 1 {
		t.Fatalf("got %d providers", len(active))
	}
	// 未登录用户看得到的列表里不能带任何配置。
	if active[0].Config != nil {
		t.Fatalf("public provider list leaks config: %+v", active[0].Config)
	}
}
