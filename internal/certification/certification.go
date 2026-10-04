// Package certification 实现实名认证（对应魔方 public/plugins/certification/）。
//
// 分两层：
//   - 通道（Provider）负责把「姓名 + 证件号」送到三方核验，或标记为人工审核
//   - 本文件里的纯函数负责**本地预处理**：校验身份证校验位、解出生日期与性别、掩码
//
// 为什么本地也要校验身份证：中国身份证第 18 位是 GB 11643-1999 定义的校验位。
// 本地先算一遍可以挡掉绝大多数「打错一位」，省下一次付费的三方调用，也避免把明显
// 非法的号码送给上游。
package certification

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Subject 是一次待核验的实名信息。
type Subject struct {
	RealName string
	IDNumber string
	// IDType 为 idcard / passport / license。
	IDType string
}

// Result 是核验结果。
type Result struct {
	// Match 表示姓名与证件号是否一致。
	Match bool
	// Gender 与 BirthDate 是通道返回或本地解析出的信息。
	Gender    string
	BirthDate string
	// Message 是通道的原文说明，便于排查。
	Message string
}

// Provider 是一个实名核验通道。
type Provider interface {
	Name() string
	// Validate 在保存配置时做静态校验，不必真的调用上游。
	Validate(cfg Config, secret Secret) error
	// Verify 发起核验。返回 Match=false 且 err=nil 表示「查到了但不一致」。
	Verify(ctx context.Context, cfg Config, secret Secret, subject Subject) (Result, error)
}

// Config 是通道的非敏感配置。
type Config struct {
	Provider string            `json:"provider"`
	Fields   map[string]string `json:"fields"`
}

// Field 安全地取一个配置项。
func (c Config) Field(key string) string {
	if c.Fields == nil {
		return ""
	}
	return strings.TrimSpace(c.Fields[key])
}

// Secret 是通道凭据。
type Secret map[string]string

// Get 安全地取一个凭据项。
func (s Secret) Get(key string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(s[key])
}

var (
	regMu sync.RWMutex
	reg   = map[string]Provider{}
)

// Register 注册一个通道实现。
func Register(p Provider) {
	regMu.Lock()
	defer regMu.Unlock()
	reg[strings.ToLower(p.Name())] = p
}

// Get 按标识取通道实现。
func Get(name string) (Provider, bool) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := reg[strings.ToLower(strings.TrimSpace(name))]
	return p, ok
}

// Names 列出已注册的通道标识。
func Names() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]string, 0, len(reg))
	for name := range reg {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ErrInvalidIDNumber 表示本地校验就没通过，不必调用上游。
var ErrInvalidIDNumber = errors.New("身份证号格式或校验位不正确")

// ---- 本地校验与解析 ----

// idWeights 是 GB 11643-1999 定义的前 17 位加权因子。
var idWeights = [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}

// idCheckCodes 是「加权和 % 11」到校验位的映射表。
var idCheckCodes = [11]byte{'1', '0', 'X', '9', '8', '7', '6', '5', '4', '3', '2'}

// ValidateChinaIDCard 校验 18 位身份证号：长度、字符、出生日期合法性、校验位。
// 15 位老号也接受（没有校验位），但会统一转换成 18 位后再判断。
func ValidateChinaIDCard(id string) error {
	id = strings.ToUpper(strings.TrimSpace(id))
	switch len(id) {
	case 15:
		// 15 位没有校验位，只校验格式与出生日期。
		if !allDigits(id) {
			return ErrInvalidIDNumber
		}
		if _, err := parseIDBirthDate(id); err != nil {
			return err
		}
		return nil
	case 18:
	default:
		return fmt.Errorf("%w：长度应为 18 位（或 15 位老号）", ErrInvalidIDNumber)
	}
	if !allDigits(id[:17]) {
		return fmt.Errorf("%w：前 17 位必须是数字", ErrInvalidIDNumber)
	}
	last := id[17]
	if !(last >= '0' && last <= '9') && last != 'X' {
		return fmt.Errorf("%w：最后一位必须是数字或 X", ErrInvalidIDNumber)
	}
	if _, err := parseIDBirthDate(id); err != nil {
		return err
	}
	sum := 0
	for i := 0; i < 17; i++ {
		sum += int(id[i]-'0') * idWeights[i]
	}
	want := idCheckCodes[sum%11]
	if last != want {
		return fmt.Errorf("%w：校验位应为 %c", ErrInvalidIDNumber, want)
	}
	return nil
}

// allDigits 判断字符串是否全为数字。
func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// parseIDBirthDate 从身份证号解析出生日期，并检查日期真实存在。
// 15 位号出生年在第 7-12 位（YYMMDD），18 位在第 7-14 位（YYYYMMDD）。
func parseIDBirthDate(id string) (time.Time, error) {
	var y, m, d string
	switch len(id) {
	case 15:
		y = "19" + id[6:8]
		m = id[8:10]
		d = id[10:12]
	case 18:
		y = id[6:10]
		m = id[10:12]
		d = id[12:14]
	default:
		return time.Time{}, ErrInvalidIDNumber
	}
	year, err := strconv.Atoi(y)
	if err != nil {
		return time.Time{}, ErrInvalidIDNumber
	}
	month, err := strconv.Atoi(m)
	if err != nil {
		return time.Time{}, ErrInvalidIDNumber
	}
	day, err := strconv.Atoi(d)
	if err != nil {
		return time.Time{}, ErrInvalidIDNumber
	}
	// time.Date 会把 2 月 30 日「归一化」成 3 月 2 日，所以必须回读比对，
	// 否则 20230230 会被当成合法日期放过。
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if t.Year() != year || int(t.Month()) != month || t.Day() != day {
		return time.Time{}, fmt.Errorf("%w：出生日期 %s-%s-%s 不存在", ErrInvalidIDNumber, y, m, d)
	}
	if t.After(time.Now()) {
		return time.Time{}, fmt.Errorf("%w：出生日期不能是未来", ErrInvalidIDNumber)
	}
	return t, nil
}

// GenderFromIDCard 从身份证号取性别：第 17 位奇数为男、偶数为女。
// 15 位号同理取第 15 位（此处已统一按 15 位规则处理）。
func GenderFromIDCard(id string) string {
	id = strings.TrimSpace(id)
	var idx int
	switch len(id) {
	case 15:
		idx = 14
	case 18:
		idx = 16
	default:
		return ""
	}
	d := id[idx]
	if d < '0' || d > '9' {
		return ""
	}
	if (d-'0')%2 == 1 {
		return "male"
	}
	return "female"
}

// BirthDateFromIDCard 返回 YYYY-MM-DD 形式的出生日期，解析失败返回空串。
func BirthDateFromIDCard(id string) string {
	t, err := parseIDBirthDate(strings.TrimSpace(id))
	if err != nil {
		return ""
	}
	return t.Format("2006-01-02")
}

// MaskName 把姓名掩码成「张*三」「张**」这种形式。
// 保留首尾字符便于本人辨认，中间全部打码。
func MaskName(name string) string {
	r := []rune(strings.TrimSpace(name))
	switch len(r) {
	case 0:
		return ""
	case 1:
		return string(r)
	case 2:
		return string(r[0]) + "*"
	default:
		// 首尾保留，中间用等量星号。
		return string(r[0]) + strings.Repeat("*", len(r)-2) + string(r[len(r)-1])
	}
}

// MaskIDNumber 把证件号掩码成「110***********1234」：保留前 3 位与后 4 位。
func MaskIDNumber(id string) string {
	id = strings.TrimSpace(id)
	if len(id) <= 7 {
		if len(id) == 0 {
			return ""
		}
		return strings.Repeat("*", len(id))
	}
	return id[:3] + strings.Repeat("*", len(id)-7) + id[len(id)-4:]
}

// ---- 人工审核通道 ----

// Manual 不做任何自动核验，只把提交以 pending 落库，等管理员在后台点通过/驳回。
// 它的价值在于：没有三方通道（或不想付费）时实名认证流程依然可用。
type Manual struct{}

// Name 返回通道标识。
func (Manual) Name() string { return "manual" }

// Validate 人工通道没有必填配置。
func (Manual) Validate(Config, Secret) error { return nil }

// Verify 永远返回 Match=false：真正的判定由管理员做出。
// Message 里说清楚这不是失败，而是「待人工处理」。
func (Manual) Verify(_ context.Context, _ Config, _ Secret, _ Subject) (Result, error) {
	return Result{Match: false, Message: "人工审核通道：已提交，等待管理员审核"}, nil
}

func init() { Register(Manual{}) }
