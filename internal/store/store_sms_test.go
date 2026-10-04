package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// 短信验证码的存储与邮箱验证码同构，但这里是花钱的通道，所以重点验证
// 限频、错误锁定与发送流水。

func TestSmsCodeHappyPath(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	phone := "13800138000"
	hash := "hash-1"
	if err := s.PutSmsCode(ctx, phone, "register", hash, 10*time.Minute, time.Minute); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := s.ConsumeSmsCode(ctx, phone, "register", hash); err != nil {
		t.Fatalf("consume: %v", err)
	}
	// 验证码是一次性的：消费过就不能再用。
	if err := s.ConsumeSmsCode(ctx, phone, "register", hash); !errors.Is(err, ErrCodeInvalid) {
		t.Fatalf("re-consume = %v, want ErrCodeInvalid", err)
	}
}

func TestSmsCodeRejectsWrongCodeAndLocks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	phone := "13800138001"
	if err := s.PutSmsCode(ctx, phone, "register", "right", 10*time.Minute, 0); err != nil {
		t.Fatal(err)
	}
	// 前 5 次错码都是 Invalid。
	for i := 0; i < 5; i++ {
		if err := s.ConsumeSmsCode(ctx, phone, "register", "wrong"); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("attempt %d = %v, want ErrCodeInvalid", i+1, err)
		}
	}
	// 第 6 次必须直接锁定，即使给的是正确验证码。
	if err := s.ConsumeSmsCode(ctx, phone, "register", "right"); !errors.Is(err, ErrCodeLocked) {
		t.Fatalf("after 5 failures = %v, want ErrCodeLocked", err)
	}
}

func TestSmsCodeRateLimitAndExpiry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	phone := "13800138002"
	if err := s.PutSmsCode(ctx, phone, "register", "h1", 10*time.Minute, time.Minute); err != nil {
		t.Fatal(err)
	}
	// 重发窗口内再次发送必须被限频。
	if err := s.PutSmsCode(ctx, phone, "register", "h2", 10*time.Minute, time.Minute); !errors.Is(err, ErrCodeRateLimited) {
		t.Fatalf("resend within window = %v, want ErrCodeRateLimited", err)
	}
	// 换一个用途不受影响（限频是按 手机号+用途 计的）。
	if err := s.PutSmsCode(ctx, phone, "login", "h3", 10*time.Minute, time.Minute); err != nil {
		t.Fatalf("different purpose should not be rate limited: %v", err)
	}
	// 已经过期的验证码不能再用。
	phone2 := "13800138003"
	if err := s.PutSmsCode(ctx, phone2, "register", "h4", -time.Second, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeSmsCode(ctx, phone2, "register", "h4"); !errors.Is(err, ErrCodeExpired) {
		t.Fatalf("expired code = %v, want ErrCodeExpired", err)
	}
}

func TestNormalizePhoneStripsSeparators(t *testing.T) {
	cases := map[string]string{
		"138 0013 8000":     "13800138000",
		"+86-138-0013-8000": "8613800138000",
		"(138) 0013-8000":   "13800138000",
		"  13800138000  ":   "13800138000",
	}
	for in, want := range cases {
		if got := normalizePhone(in); got != want {
			t.Fatalf("normalizePhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSmsSendStatsCountsOnlySuccesses(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	phone := "13800138004"
	// 两次成功 + 一次失败：风控只看成功数（失败的短信不花钱）。
	s.RecordSmsMessage(ctx, phone, "register", "aliyun", "SMS_1", true, "", 0, "127.0.0.1")
	s.RecordSmsMessage(ctx, phone, "register", "aliyun", "SMS_1", true, "", 0, "127.0.0.1")
	s.RecordSmsMessage(ctx, phone, "register", "aliyun", "SMS_1", false, "boom", 0, "127.0.0.1")
	st, err := s.SmsSendStats(ctx, phone)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastMinute != 2 || st.LastHour != 2 || st.LastDay != 2 {
		t.Fatalf("stats = %+v, want 2 successes counted", st)
	}
}

func TestSmsProviderDefaultIsExclusive(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, err := s.CreateSmsProvider(ctx, "通道A", "aliyun", map[string]string{"sign_name": "A"}, "enc-a", true)
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	if !a.IsDefault {
		t.Fatal("the first provider should be the default")
	}
	b, err := s.CreateSmsProvider(ctx, "通道B", "aliyun", map[string]string{"sign_name": "B"}, "enc-b", true)
	if err != nil {
		t.Fatalf("create B: %v", err)
	}
	// 唯一索引只允许一个默认通道：后设置的成为默认，前一个自动取消。
	list, err := s.ListSmsProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defaults := 0
	for _, pr := range list {
		if pr.IsDefault {
			defaults++
			if pr.PublicID != b.PublicID {
				t.Fatalf("default is %s, want the newest %s", pr.PublicID, b.PublicID)
			}
		}
	}
	if defaults != 1 {
		t.Fatalf("found %d default providers, want exactly 1", defaults)
	}
	// 默认通道必须是可用的那个。
	active, _, err := s.ActiveSmsProvider(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if active.PublicID != b.PublicID {
		t.Fatalf("active provider = %s, want %s", active.PublicID, b.PublicID)
	}
	_ = a
}
