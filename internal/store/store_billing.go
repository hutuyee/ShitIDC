package store

import (
	"fmt"
	"strings"
)

// 计费模型（魔方 pay_type 的 Go 实现）：周期 / 一次性 / 免费 / 试用，
// 以及多周期价格。

// PayType 与收银逻辑的关系：
//
//	recurring 周期付费，下单后需支付
//	onetime   一次性，只有一个 onetime 价格档
//	free      免费，$0 订单，下单即开通（不走支付）
//	trial     试用，可收一笔试用费；开通后 trial_days 天自动回收
const (
	PayTypeRecurring = "recurring"
	PayTypeOneTime   = "onetime"
	PayTypeFree      = "free"
	PayTypeTrial     = "trial"
)

// BillingCycles 是所有允许的计费周期，顺序即前台展示顺序。
// 长周期与魔方 shd_pricing 的列一一对应（biennially..tenly）。
var BillingCycles = []string{
	"hourly", "daily", "monthly", "quarterly", "semiannually", "yearly",
	"biennially", "triennially", "fourly", "fively", "sixly", "sevenly", "eightly", "ninely", "tenly",
	"onetime",
}

// CycleMonths 把一个周期折算成月数；onetime 返回 0（不计入周期续费）。
func CycleMonths(cycle string) int {
	switch cycle {
	case "hourly", "daily":
		return 1
	case "monthly":
		return 1
	case "quarterly":
		return 3
	case "semiannually":
		return 6
	case "yearly":
		return 12
	case "biennially":
		return 24
	case "triennially":
		return 36
	case "fourly":
		return 48
	case "fively":
		return 60
	case "sixly":
		return 72
	case "sevenly":
		return 84
	case "eightly":
		return 96
	case "ninely":
		return 108
	case "tenly":
		return 120
	default:
		return 0
	}
}

// ValidBillingCycle 判断周期是否受支持。
func ValidBillingCycle(cycle string) bool {
	for _, c := range BillingCycles {
		if c == cycle {
			return true
		}
	}
	return false
}

// ValidPayType 判断计费类型是否受支持。
func ValidPayType(t string) bool {
	switch t {
	case PayTypeRecurring, PayTypeOneTime, PayTypeFree, PayTypeTrial:
		return true
	}
	return false
}

// PriceInput 是新建/更新商品时提交的一个价格档。
type PriceInput struct {
	BillingCycle string
	AmountCents  int64
	// Currency 留空表示用调用方给的默认币种；填了就按该币种单独建价格行
	// （多币种独立定价：每个币种一套自己的价，不用汇率折算）。
	Currency string
}

// ProductBillingInput 是商品的计费配置。
type ProductBillingInput struct {
	PayType           string
	TrialDays         int
	TrialPriceCents   int64
	AutoTerminateDays int
	// 库存与限购
	StockControl   bool
	StockQty       int
	AllowQty       bool
	MaxPerCustomer int
	IsFeatured     bool
}

// nullablePayType 把空字符串转成“不改动”。
func normalizePayType(t string) (string, error) {
	t = strings.TrimSpace(strings.ToLower(t))
	if t == "" {
		return "", nil
	}
	if !ValidPayType(t) {
		return "", fmt.Errorf("计费类型仅支持 recurring / onetime / free / trial")
	}
	return t, nil
}

// validatePrices 校验一组价格档：周期合法、金额非负、免费/一次性商品的规则。
func validatePrices(payType string, prices []PriceInput) error {
	if len(prices) == 0 {
		return fmt.Errorf("请至少设置一个价格档")
	}
	// 唯一键是（周期，币种）：多币种独立定价下，同一个周期在每个币种上都应该有一条，
	// 所以只有「同周期同币种」重复才算错。
	seen := map[string]bool{}
	seenCycle := map[string]bool{}
	for _, pr := range prices {
		cycle := strings.TrimSpace(strings.ToLower(pr.BillingCycle))
		if !ValidBillingCycle(cycle) {
			return fmt.Errorf("不支持的计费周期：%s", pr.BillingCycle)
		}
		currency := strings.ToUpper(strings.TrimSpace(pr.Currency))
		key := cycle + "|" + currency
		if seen[key] {
			if currency == "" {
				return fmt.Errorf("计费周期 %s 重复", cycle)
			}
			return fmt.Errorf("计费周期 %s 在 %s 下重复", cycle, currency)
		}
		seen[key] = true
		seenCycle[cycle] = true
		if pr.AmountCents < 0 {
			return fmt.Errorf("价格不能为负数")
		}
	}
	switch payType {
	case PayTypeOneTime:
		if !seenCycle["onetime"] {
			return fmt.Errorf("一次性商品必须有一个 onetime 价格档")
		}
	case PayTypeFree:
		// 免费商品价格必须是 0（可以只留一个月付档占位）。
		for _, pr := range prices {
			if pr.AmountCents != 0 {
				return fmt.Errorf("免费商品的价格必须为 0")
			}
		}
	}
	return nil
}
