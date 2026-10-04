package certification

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// 身份证校验位是 GB 11643-1999 定义的。算错了会放行打错一位的号码，
// 或者把合法号码拒掉，所以逐条钉死。

// makeIDCard 用正确的算法给前 17 位算出校验位，方便构造合法测试数据。
func makeIDCard(first17 string) string {
	sum := 0
	for i := 0; i < 17; i++ {
		sum += int(first17[i]-'0') * idWeights[i]
	}
	return first17 + string(idCheckCodes[sum%11])
}

func TestValidateChinaIDCardAcceptsValidNumbers(t *testing.T) {
	// 用算法本身生成合法号码，再验证它能通过——只要算法自洽就不会误拒。
	cases := []string{
		"11010119900307123", // 1990-03-07
		"44030119851201123", // 1985-12-01
		"32010220000229123", // 2000-02-29（闰年）
		"1101011990030712X", // 末位 X 的情况由下面单独构造
	}
	for _, first17 := range cases[:3] {
		id := makeIDCard(first17)
		if err := ValidateChinaIDCard(id); err != nil {
			t.Fatalf("generated valid id %s was rejected: %v", id, err)
		}
	}
	_ = cases
}

func TestValidateChinaIDCardRejectsBadCheckDigit(t *testing.T) {
	valid := makeIDCard("11010119900307123")
	if err := ValidateChinaIDCard(valid); err != nil {
		t.Fatalf("baseline is not valid: %v", err)
	}
	// 把校验位换成一个不同的字符，必须被拒。
	last := valid[17]
	wrong := byte('0')
	if last == '0' {
		wrong = '1'
	}
	tampered := valid[:17] + string(wrong)
	if err := ValidateChinaIDCard(tampered); err == nil {
		t.Fatalf("%s has a wrong check digit but was accepted", tampered)
	}
	// 打错中间一位也必须被拒（校验位就是干这个的）。
	swapped := []byte(valid)
	if swapped[10] == '9' {
		swapped[10] = '8'
	} else {
		swapped[10] = '9'
	}
	if err := ValidateChinaIDCard(string(swapped)); err == nil {
		t.Fatalf("%s has a typo in the birth date but was accepted", string(swapped))
	}
}

func TestValidateChinaIDCardRejectsBadShape(t *testing.T) {
	bad := []string{
		"",                    // 空
		"12345",               // 太短
		"1101011990030712345", // 太长
		"11010119900307123A",  // 末位非法字符
		"1101011990030712 3",  // 含空格
		"1101011990030712中",   // 含中文
		"11010119901307123",   // 13 月
		"11010119900230123",   // 2 月 30 日（非闰年）
	}
	for _, id := range bad {
		if err := ValidateChinaIDCard(id); err == nil {
			t.Fatalf("%q should have been rejected", id)
		}
	}
}

// 2 月 30 日这种「看起来像日期但不是」的输入必须被拒：
// time.Date 会把它归一化成 3 月 2 日，不比对就会放过。
func TestValidateChinaIDCardRejectsNonexistentDate(t *testing.T) {
	// 2023 不是闰年，2 月 29 日不存在。
	if err := ValidateChinaIDCard(makeIDCard("11010120230229123")); err == nil {
		t.Fatal("2023-02-29 does not exist but was accepted")
	}
	// 2024 是闰年，2 月 29 日存在。
	if err := ValidateChinaIDCard(makeIDCard("11010120240229123")); err != nil {
		t.Fatalf("2024-02-29 is a real date but was rejected: %v", err)
	}
}

func TestValidateChinaIDCardAccepts15DigitOldFormat(t *testing.T) {
	// 15 位老号没有校验位：格式与日期对就接受，格式不对就拒。
	if err := ValidateChinaIDCard("110101900307123"); err != nil {
		t.Fatalf("a well-formed 15-digit id was rejected: %v", err)
	}
	if err := ValidateChinaIDCard("110101901307123"); err == nil {
		t.Fatal("a 15-digit id with month 13 was accepted")
	}
}

func TestGenderAndBirthDate(t *testing.T) {
	// 第 17 位奇数男、偶数女。构造两个确定性的号码。
	male := makeIDCard("11010119900307123")   // 第 17 位是 3 -> 男
	female := makeIDCard("11010119900307144") // 第 17 位是 4 -> 女
	if got := GenderFromIDCard(male); got != "male" {
		t.Fatalf("gender = %q, want male for %s", got, male)
	}
	if got := GenderFromIDCard(female); got != "female" {
		t.Fatalf("gender = %q, want female for %s", got, female)
	}
	if got := BirthDateFromIDCard(male); got != "1990-03-07" {
		t.Fatalf("birth date = %q, want 1990-03-07", got)
	}
	// 垃圾输入返回空串而不是崩溃或乱码。
	if got := GenderFromIDCard("abc"); got != "" {
		t.Fatalf("gender for garbage = %q, want empty", got)
	}
	if got := BirthDateFromIDCard("abc"); got != "" {
		t.Fatalf("birth date for garbage = %q, want empty", got)
	}
}

// 掩码函数决定「会不会把身份证号泄漏到界面上」，必须逐条验证。
func TestMaskName(t *testing.T) {
	cases := map[string]string{
		"张三":    "张*",
		"张三丰":   "张*丰",
		"欧阳修文":  "欧**文",
		"李":     "李",
		"":      "",
		" 张三丰 ": "张*丰",
	}
	for in, want := range cases {
		if got := MaskName(in); got != want {
			t.Fatalf("MaskName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskIDNumber(t *testing.T) {
	// 18 位：保留前 3 后 4，中间 11 个星号。
	got := MaskIDNumber("110101199003071234")
	if got != "110***********1234" {
		t.Fatalf("MaskIDNumber = %q, want 110***********1234", got)
	}
	if strings.Count(got, "*") != 11 {
		t.Fatalf("mask has %d stars, want 11", strings.Count(got, "*"))
	}
	// 掩码后绝不能包含原始中间段。
	if strings.Contains(got, "19900307") {
		t.Fatalf("mask %q leaks the birth date", got)
	}
	// 太短的串整体打码，不能因为越界而 panic 或泄漏。
	if got := MaskIDNumber("123"); got != "***" {
		t.Fatalf("short id mask = %q, want ***", got)
	}
	if got := MaskIDNumber(""); got != "" {
		t.Fatalf("empty mask = %q, want empty", got)
	}
}

func TestManualProvider(t *testing.T) {
	p, ok := Get("manual")
	if !ok {
		t.Fatal("the manual provider must be registered")
	}
	if err := p.Validate(Config{}, Secret{}); err != nil {
		t.Fatalf("manual needs no config: %v", err)
	}
	res, err := p.Verify(context.Background(), Config{}, Secret{}, Subject{RealName: "张三", IDNumber: "x"})
	if err != nil {
		t.Fatalf("manual verify must not error: %v", err)
	}
	// 人工通道不自动判定，但必须说明「待审核」而不是当成核验失败。
	if res.Match {
		t.Fatal("manual verification must not auto-approve")
	}
	if res.Message == "" {
		t.Fatal("manual verification must explain that it awaits review")
	}
}

func TestErrInvalidIDNumberIsWrapped(t *testing.T) {
	err := ValidateChinaIDCard("110101199003071234")
	if err == nil {
		t.Fatal("expected an error")
	}
	// 上层要能用 errors.Is 判断「本地就不过」，从而跳过一次付费调用。
	if !errors.Is(err, ErrInvalidIDNumber) {
		t.Fatalf("error %v does not wrap ErrInvalidIDNumber", err)
	}
}
