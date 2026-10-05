package payment

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"sort"
	"strings"
)

// GlobalAliPayGateway 实现支付宝国际支付（境外收单，对应魔方 CBAP 插件
// gateway/global_ali_pay）：
//
//	GET https://intlmapi.alipay.com/gateway.do?service=create_forex_trade[_wap]&...
//	MD5 签名：参数（去掉 sign/sign_type/空值）按名排序拼 k=v&k=v，末尾直接拼密钥
//
// Provider fields:
//   - MerchantID: 支付宝用户 ID（partner，2088 开头）
//   - GatewayURL: 可选覆盖，默认 https://intlmapi.alipay.com/gateway.do
//   - Secret:     JSON {"key":"MD5 密钥","currency":"HKD","rate":"0.91"}
//     也接受纯密钥字符串（currency 默认 HKD，rate 默认 1）
//
// 汇率 rate = 收取货币 / 系统货币：下单按 total_fee = 金额 × rate 传两位小数
// （与 bcmul 一样的截断），回调再按 total_fee ÷ rate 还原成系统货币分。
// 与参考实现的两处差异：回调校验 trade_status（插件只验签名，WAIT_BUYER_PAY
// 也会被当成支付成功）；金额还原用四舍五入而不是 bcdiv 截断，避免 ±1 分漂移
// 导致订单金额比对失败。
type GlobalAliPayGateway struct{}

// Method 返回支付方式标识。
func (GlobalAliPayGateway) Method() string { return "global_alipay" }

func init() { Register(GlobalAliPayGateway{}) }

type globalAliPaySecret struct {
	Key      string
	Currency string
	Rate     *big.Rat
}

// parseGlobalAliPaySecret 解析凭据：JSON 或纯密钥字符串。
func parseGlobalAliPaySecret(raw string) (globalAliPaySecret, error) {
	trimmed := strings.TrimSpace(raw)
	var fields struct {
		Key      string `json:"key"`
		Currency string `json:"currency"`
		Rate     string `json:"rate"`
	}
	if strings.HasPrefix(trimmed, "{") {
		if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
			return globalAliPaySecret{}, errors.New("global_alipay 密钥不是合法 JSON")
		}
	} else {
		fields.Key = trimmed
	}
	if strings.TrimSpace(fields.Key) == "" {
		return globalAliPaySecret{}, errors.New("global_alipay 缺少 MD5 密钥（key）")
	}
	currency := strings.ToUpper(strings.TrimSpace(fields.Currency))
	if currency == "" {
		currency = "HKD"
	}
	rateStr := strings.TrimSpace(fields.Rate)
	if rateStr == "" {
		rateStr = "1"
	}
	rate, ok := new(big.Rat).SetString(rateStr)
	if !ok || rate.Sign() <= 0 {
		return globalAliPaySecret{}, errors.New("global_alipay 的 rate 必须是正数")
	}
	return globalAliPaySecret{Key: strings.TrimSpace(fields.Key), Currency: currency, Rate: rate}, nil
}

// globalAliPaySignContent 复刻参考实现的 paraFilter + argSort + createLinkstring：
// 去掉 sign/sign_type/空值，按参数名排序，拼 k=v&k=v（值不做转义，直接连接）。
func globalAliPaySignContent(params url.Values) string {
	keys := make([]string, 0, len(params))
	for k, vs := range params {
		if k == "sign" || k == "sign_type" || len(vs) == 0 || strings.TrimSpace(vs[0]) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(k)
		b.WriteByte('=')
		b.WriteString(params.Get(k))
	}
	return b.String()
}

// globalAliPaySign 计算 MD5 签名：md5(排序串 + key)。
func globalAliPaySign(params url.Values, key string) string {
	sum := md5.Sum([]byte(globalAliPaySignContent(params) + key))
	return hex.EncodeToString(sum[:])
}

// siteOrigin 取站点根地址（scheme://host），refer_url 用。
func siteOrigin(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// payRateMulCents 返回 cents × rate，截断到分（bcmul(...,2) 的语义）。
func payRateMulCents(cents int64, rate *big.Rat) int64 {
	scaled := new(big.Rat).Mul(new(big.Rat).SetInt64(cents), rate)
	return new(big.Int).Quo(scaled.Num(), scaled.Denom()).Int64()
}

// payRateDivCents 返回 feeCents ÷ rate，四舍五入到分。
func payRateDivCents(feeCents int64, rate *big.Rat) int64 {
	scaled := new(big.Rat).Quo(new(big.Rat).SetInt64(feeCents), rate)
	scaled.Add(scaled, big.NewRat(1, 2))
	return new(big.Int).Quo(scaled.Num(), scaled.Denom()).Int64()
}

// payCentsToYuan 渲染两位小数字符串（12.30）。
func payCentsToYuan(cents int64) string {
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

// PayURL 组装境外收单跳转地址（GET）；wap 渠道用 create_forex_trade_wap。
func (g GlobalAliPayGateway) PayURL(_ context.Context, cfg ProviderConfig, p Prepared) (string, error) {
	s, err := parseGlobalAliPaySecret(cfg.Secret)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cfg.MerchantID) == "" {
		return "", errors.New("支付宝国际支付需要 partner（2088 开头的商户用户 ID）")
	}
	if p.AmountCents <= 0 {
		return "", errors.New("支付金额必须大于 0")
	}
	subject := strings.TrimSpace(p.Subject)
	if subject == "" {
		subject = "订单" + p.OutTradeNo
	}
	// 参考实现会替换文案里的「服务费」——国际收银台按英文展示。
	subject = strings.ReplaceAll(subject, "服务费", " Service Fee")
	service := "create_forex_trade"
	productCode := "NEW_OVERSEAS_SELLER"
	if strings.Contains(strings.ToLower(p.PayType), "wap") {
		service = "create_forex_trade_wap"
		productCode = "NEW_WAP_OVERSEAS_SELLER"
	}
	tradeInfo, _ := json.Marshal(map[string]any{"business_type": 5, "other_business_type": subject})
	params := url.Values{
		"service":           {service},
		"partner":           {cfg.MerchantID},
		"notify_url":        {p.NotifyURL},
		"return_url":        {p.ReturnURL},
		"refer_url":         {siteOrigin(p.NotifyURL)},
		"_input_charset":    {"utf-8"},
		"out_trade_no":      {p.OutTradeNo},
		"subject":           {subject},
		"body":              {subject},
		"currency":          {s.Currency},
		"total_fee":         {payCentsToYuan(payRateMulCents(p.AmountCents, s.Rate))},
		"product_code":      {productCode},
		"trade_information": {string(tradeInfo)},
		"qr_pay_mode":       {"4"},
		"qrcode_width":      {"200"},
	}
	// 空值不参与签名也不发出去（参考实现的 paraFilter 语义）。
	for k, vs := range params {
		if len(vs) == 0 || strings.TrimSpace(vs[0]) == "" {
			params.Del(k)
		}
	}
	params.Set("sign", globalAliPaySign(params, s.Key))
	params.Set("sign_type", "MD5")
	base := strings.TrimRight(cfg.GatewayURL, "/")
	if base == "" {
		base = "https://intlmapi.alipay.com/gateway.do"
	}
	return base + "?" + params.Encode(), nil
}

// VerifyNotify 校验异步通知（POST）或同步返回（GET）：MD5 签名 + trade_status，
// 金额按汇率还原成系统货币分返回给上层比对。
func (g GlobalAliPayGateway) VerifyNotify(cfg ProviderConfig, input NotifyInput) NotifyResult {
	fail := func(err error) NotifyResult { return NotifyResult{OK: false, Err: err} }
	s, err := parseGlobalAliPaySecret(cfg.Secret)
	if err != nil {
		return fail(err)
	}
	values := input.Values()
	sign := values.Get("sign")
	if sign == "" {
		return fail(errors.New("回调缺少 sign"))
	}
	if !strings.EqualFold(globalAliPaySign(values, s.Key), sign) {
		return fail(errors.New("支付宝国际支付回调签名验证失败"))
	}
	status := values.Get("trade_status")
	if status != "TRADE_SUCCESS" && status != "TRADE_FINISHED" {
		return fail(fmt.Errorf("trade_status %q 不代表支付成功", status))
	}
	feeCents, err := alipayYuanToCents(values.Get("total_fee"))
	if err != nil {
		return fail(err)
	}
	return NotifyResult{
		OK:          true,
		TradeNo:     values.Get("trade_no"),
		AmountCents: payRateDivCents(feeCents, s.Rate),
	}
}
