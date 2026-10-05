// Package payment —— 虎皮椒（XunhuPay）聚合支付网关。
//
// 对应魔方 public/plugins/gateways/hpj_alipay_pay 与 hpj_wechat_pay：两个插件
// 用的是同一套协议（同一个 lib/XunhupayClient），只有 payment 字段
// （alipay / wechat）不同。这里按**一个网关方法**实现，渠道由下单时的
// PayType 选择——与易支付的接入方式一致，管理员给两种渠道各建一条
// 支付方式记录即可。
//
// 协议（官方文档 + 参考插件源码）：
//
//	下单  POST {gateway}/payment/do.html   JSON 体
//	     {version:"1.1", trade_order_id, payment, total_fee, title,
//	      notify_url, return_url, appid, time, nonce_str, hash}
//	     响应 {errcode:0, errmsg, url:收银台地址, hash}
//	回调  POST form 到 notify_url，含 hash / trade_order_id / transaction_id /
//	     status("OD"=已支付) / total_fee；验签通过回 "success"
//	签名  参数按名排序拼 k=v&k=v（排除 hash 与空值），末尾拼接 AppSecret，
//	     取 MD5 小写。**没有分隔符**，secret 直接接在串尾。
//
// 两个刻意与参考实现不同的地方：
//  1. 参考实现校验响应签名时写成了 `$hash !== $hash`（恒 false，验签形同虚设），
//     这里真的校验响应 hash，失败即报错；
//  2. 金额用「分」精确换算成两位小数字符串，不走浮点。
package payment

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// XunhuPayGateway 实现虎皮椒协议。
type XunhuPayGateway struct {
	// Now / Nonce 允许测试注入，保证签名可复算。
	Now   func() time.Time
	Nonce func() string
	// HTTPClient 可注入（测试指向本地假网关）。
	HTTPClient *http.Client
}

// Method 返回网关标识。
func (XunhuPayGateway) Method() string { return "xunhupay" }

func init() { Register(XunhuPayGateway{Now: time.Now}) }

// xunhuHash 计算虎皮椒 hash：参数按名排序拼 k=v&k=v（排除 hash 与空值），
// 末尾拼接 AppSecret 后取 MD5 小写。
func xunhuHash(params url.Values, appSecret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "hash" || params.Get(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params.Get(k))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + appSecret))
	return hex.EncodeToString(sum[:])
}

// xunhuMoney 把分换算成两位小数字符串（total_fee 要求 "12.30" 形态）。
func xunhuMoney(cents int64) string {
	return strconv.FormatInt(cents/100, 10) + "." + fmt.Sprintf("%02d", cents%100)
}

// xunhuYuanToCents 把两位小数的元字符串换算回分（回调金额比对用）。
// 走整数运算：解析成「元 + 两位小数」，避免浮点误差。
func xunhuYuanToCents(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, fmt.Errorf("金额为空")
	}
	parts := strings.SplitN(v, ".", 2)
	yuan, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || yuan < 0 {
		return 0, fmt.Errorf("金额格式无效")
	}
	cents := yuan * 100
	if len(parts) == 2 {
		frac := parts[1]
		if len(frac) != 2 || !allDigitsPay(frac) {
			return 0, fmt.Errorf("金额格式无效")
		}
		d, _ := strconv.ParseInt(frac, 10, 64)
		cents += d
	}
	return cents, nil
}

func allDigitsPay(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// PayURL 调 /payment/do.html 下单，返回收银台跳转地址。
// PayType 决定 payment 字段：alipay / wechat（其他值按 wechat 提交会失败，
// 所以这里白名单校验）。
func (g XunhuPayGateway) PayURL(ctx context.Context, cfg ProviderConfig, p Prepared) (string, error) {
	if cfg.MerchantID == "" || cfg.Secret == "" {
		return "", fmt.Errorf("虎皮椒需要 AppID（商户 ID）与 AppSecret（密钥）")
	}
	payType := strings.ToLower(strings.TrimSpace(p.PayType))
	if payType == "wxpay" {
		// 前端渠道下拉的微信值是 wxpay，虎皮椒协议里是 wechat。
		payType = "wechat"
	}
	if payType != "alipay" && payType != "wechat" {
		return "", fmt.Errorf("虎皮椒支付方式只支持 alipay / wechat，当前 %q", p.PayType)
	}
	if p.AmountCents <= 0 {
		return "", fmt.Errorf("支付金额必须大于 0")
	}
	secret, err := parseXunhuSecret(cfg.Secret)
	if err != nil {
		return "", err
	}
	now := g.now()
	params := url.Values{
		"version":        {"1.1"},
		"trade_order_id": {p.OutTradeNo},
		"payment":        {payType},
		"total_fee":      {xunhuMoney(p.AmountCents)},
		"title":          {p.Subject},
		"notify_url":     {p.NotifyURL},
		"return_url":     {p.ReturnURL},
		"appid":          {cfg.MerchantID},
		"time":           {strconv.FormatInt(now.Unix(), 10)},
		"nonce_str":      {g.nonce()},
	}
	params.Set("hash", xunhuHash(params, secret.AppSecret))

	base := strings.TrimRight(cfg.GatewayURL, "/")
	if base == "" {
		base = "https://api.xunhupay.com"
	}
	// 与参考实现一致：JSON 体（curl_post(json_encode($params))）。
	// url.Values 直接 Marshal 会产出数组值，必须展开成标量 map。
	scalar := make(map[string]string, len(params))
	for k := range params {
		scalar[k] = params.Get(k)
	}
	jsonBody, err := json.Marshal(scalar)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/payment/do.html", bytes.NewReader(jsonBody))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := g.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", ErrGatewayAPI{Detail: "虎皮椒下单请求失败: " + err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	// 网关对数值字段可能回数字也可能回字符串，宽容解码。
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return "", ErrGatewayAPI{Detail: fmt.Sprintf("虎皮椒返回无法解析(HTTP %d): %s", resp.StatusCode, truncatePayLog(body))}
	}
	get := func(k string) string { return scalarToString(raw[k]) }
	if errcode := get("errcode"); errcode != "" && errcode != "0" {
		return "", ErrGatewayAPI{Detail: "虎皮椒下单失败: " + firstNonEmptyPay(get("errmsg"), "未知错误")}
	}
	// 参考实现的响应验签恒通过（$hash !== $hash 笔误），这里真校验。
	if hash := get("hash"); hash != "" && get("url") != "" {
		if xunhuHash(valuesOfResponse(body), secret.AppSecret) != hash {
			return "", ErrGatewayAPI{Detail: "虎皮椒下单返回签名校验失败"}
		}
	}
	if get("url") == "" {
		return "", ErrGatewayAPI{Detail: "虎皮椒下单成功但未返回收银台地址"}
	}
	return get("url"), nil
}

// VerifyNotify 校验异步回调：hash 验签 + status=="OD" + 金额。
func (g XunhuPayGateway) VerifyNotify(cfg ProviderConfig, input NotifyInput) NotifyResult {
	fail := func(err error) NotifyResult { return NotifyResult{OK: false, Err: err} }
	secret, err := parseXunhuSecret(cfg.Secret)
	if err != nil {
		return fail(err)
	}
	values := input.Values()
	if values.Get("hash") == "" {
		return fail(fmt.Errorf("回调缺少 hash"))
	}
	if xunhuHash(values, secret.AppSecret) != values.Get("hash") {
		return fail(fmt.Errorf("虎皮椒回调签名验证失败"))
	}
	if values.Get("status") != "OD" {
		return fail(ErrTradeStatus)
	}
	cents, err := xunhuYuanToCents(values.Get("total_fee"))
	if err != nil {
		return fail(err)
	}
	return NotifyResult{OK: true, TradeNo: values.Get("transaction_id"), AmountCents: cents}
}

// xunhuSecret 是虎皮椒的凭据集：AppSecret 直接放 secret 字段（与易支付同构）。
type xunhuSecret struct{ AppSecret string }

func parseXunhuSecret(raw string) (xunhuSecret, error) {
	if strings.TrimSpace(raw) == "" {
		return xunhuSecret{}, fmt.Errorf("虎皮椒需要 AppSecret（secret 字段）")
	}
	return xunhuSecret{AppSecret: strings.TrimSpace(raw)}, nil
}

// valuesOfResponse 把响应 JSON 解回 url.Values 供验签（虎皮椒对响应同样按
// k=v 串签名，数值字段用 Go 默认格式化即可与其 MD5 复算一致的前提是——
// 网关对数值也不做特殊格式化；为稳妥起见这里直接用原始 JSON 的字符串值）。
func valuesOfResponse(body []byte) url.Values {
	var m map[string]any
	out := url.Values{}
	if err := json.Unmarshal(body, &m); err != nil {
		return out
	}
	for k, v := range m {
		out.Set(k, scalarToString(v))
	}
	return out
}

// scalarToString 把 JSON 标量转回字符串（嵌套结构原样 JSON，虎皮椒响应
// 只有标量，防御性处理）。
func scalarToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(bytes.Trim(b, `"`))
	}
}

func (g XunhuPayGateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func (g XunhuPayGateway) nonce() string {
	if g.Nonce != nil {
		return g.Nonce()
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func truncatePayLog(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

func firstNonEmptyPay(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
