// Package payment —— GoAllPay（allpayx.com 全球聚合支付）网关。
//
// 对应魔方 public/plugins/gateways/GoallpayAli / GoallpayWechat /
// GoallpayUnionpay 三个插件：同一个协议（AllPay 通用支付接口 v5），只有
// paymentMethod 字段不同（alipay_cn / wechat_pay / unionpay）。这里按
// 一个网关方法实现，渠道由 PayType 选择（wxpay 归一化为 wechat_pay）。
//
// 协议（AllPay 集成规范 v5 + 参考插件源码）：
//
//	下单  POST {gateway}/api/createorder  JSON 体
//	     {orderNum, orderCurrency, frontURL, backURL, merID, transTime(14位),
//	      signType:SHA256, orderAmount, goodsInfo, detailInfo(base64 JSON),
//	      userID, logisticsStreet, paymentMethod, version:VER000000005,
//	      charSet:UTF-8, transType:PURC, acqID:99020344, paymentSchema:AP,
//	      tradeFrom:WEB, osType:WINDOWS, signature}
//	     响应 {RespCode:"00", RespMsg, parameter:{payUrl}}
//	回调  POST form 到 backURL：GWTime/RespCode/RespMsg/acqID/charSet/merID/
//	     orderAmount/orderCurrency/orderNum/paymentSchema/signType/transID/
//	     transTime/transType/version/signature；RespCode=="00" 表示已支付。
//	签名  参数按名排序拼 k=v&k=v（**排除空值与 signature**），末尾拼接
//	     signKey，SHA256 十六进制小写。
//
// 参考实现不校验 createorder 响应的签名（RespCode 判断后直接取 payUrl），
// 这里保持一致；回调验签是真做的。userIP 不在 Prepared 里，签名规则排除
// 空值，所以直接省略该字段。
package payment

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"

	"strings"
	"time"
)

// GoallpayGateway 实现 GoAllPay 聚合支付。
type GoallpayGateway struct {
	// Now 允许测试注入，保证 transTime 可复算。
	Now func() time.Time
}

// Method 返回网关标识。
func (GoallpayGateway) Method() string { return "goallpay" }

func init() { Register(GoallpayGateway{Now: time.Now}) }

// goallpayPaymentMethod 把 PayType 映射到 AllPay 的 paymentMethod。
func goallpayPaymentMethod(payType string) string {
	switch strings.ToLower(strings.TrimSpace(payType)) {
	case "alipay":
		return "alipay_cn"
	case "wxpay", "wechat", "wechat_pay":
		return "wechat_pay"
	case "unionpay":
		return "unionpay"
	default:
		return ""
	}
}

// goallpaySign 计算 AllPay v5 签名：排序 k=v&…（排除空值与 signature）+ key，
// SHA256 十六进制小写。
func goallpaySign(params url.Values, key string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "signature" || params.Get(k) == "" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+params.Get(k))
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "&") + key))
	return hex.EncodeToString(sum[:])
}

// PayURL 调 /api/createorder 下单，返回收银台跳转地址。
func (g GoallpayGateway) PayURL(ctx context.Context, cfg ProviderConfig, p Prepared) (string, error) {
	if cfg.MerchantID == "" || cfg.Secret == "" {
		return "", fmt.Errorf("GoAllPay 需要 merchant_id（merID）与 key（签名密钥）")
	}
	payMethod := goallpayPaymentMethod(p.PayType)
	if payMethod == "" {
		return "", fmt.Errorf("GoAllPay 支付方式只支持 alipay / wxpay / unionpay，当前 %q", p.PayType)
	}
	if p.AmountCents <= 0 {
		return "", fmt.Errorf("支付金额必须大于 0")
	}
	currency := strings.ToUpper(strings.TrimSpace(p.Currency))
	if currency == "" {
		currency = "CNY"
	}
	now := g.now()
	detail, _ := json.Marshal([]map[string]string{{"goods_name": p.Subject, "quantity": "1"}})
	params := url.Values{
		"orderNum":      {p.OutTradeNo},
		"orderCurrency": {currency},
		"frontURL":      {p.ReturnURL},
		"backURL":       {p.NotifyURL},
		"merID":         {cfg.MerchantID},
		"transTime":     {now.Format("20060102150405")},
		"signType":      {"SHA256"},
		"orderAmount":   {xunhuMoney(p.AmountCents)},
		"goodsInfo":     {p.Subject},
		"detailInfo":    {base64.StdEncoding.EncodeToString(detail)},
		"paymentMethod": {payMethod},
		"version":       {"VER000000005"},
		"charSet":       {"UTF-8"},
		"transType":     {"PURC"},
		"acqID":         {"99020344"},
		"paymentSchema": {"AP"},
		"tradeFrom":     {"WEB"},
		"osType":        {"WINDOWS"},
	}
	// userID / logisticsStreet / userIP 是信息性字段，Prepared 未携带且
	// 签名规则排除空值，直接省略（对验签无影响）。
	params.Set("signature", goallpaySign(params, cfg.Secret))

	base := strings.TrimRight(cfg.GatewayURL, "/")
	if base == "" {
		base = "https://api.allpayx.com"
	}
	// 与参考实现一致：JSON 体（curlRequest(json_encode($signData))）。
	// 签名针对原始值（不做 URL 编码），JSON 体正好保持原值。
	scalar := make(map[string]string, len(params))
	for k := range params {
		scalar[k] = params.Get(k)
	}
	jsonBody, err := json.Marshal(scalar)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/createorder", strings.NewReader(string(jsonBody)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", ErrGatewayAPI{Detail: "GoAllPay 下单请求失败: " + err.Error()}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var out struct {
		RespCode  string `json:"RespCode"`
		RespMsg   string `json:"RespMsg"`
		Parameter struct {
			PayURL string `json:"payUrl"`
		} `json:"parameter"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", ErrGatewayAPI{Detail: fmt.Sprintf("GoAllPay 返回无法解析(HTTP %d): %s", resp.StatusCode, truncatePayLog(body))}
	}
	if out.RespCode != "00" {
		return "", ErrGatewayAPI{Detail: "GoAllPay 下单失败: " + firstNonEmptyPay(out.RespMsg, "未知错误")}
	}
	if out.Parameter.PayURL == "" {
		return "", ErrGatewayAPI{Detail: "GoAllPay 下单成功但未返回支付地址"}
	}
	return out.Parameter.PayURL, nil
}

// VerifyNotify 校验异步回调：签名 + RespCode=="00" + 金额。
func (g GoallpayGateway) VerifyNotify(cfg ProviderConfig, input NotifyInput) NotifyResult {
	fail := func(err error) NotifyResult { return NotifyResult{OK: false, Err: err} }
	if strings.TrimSpace(cfg.Secret) == "" {
		return fail(fmt.Errorf("GoAllPay 未配置签名密钥"))
	}
	values := input.Values()
	if values.Get("signature") == "" {
		return fail(fmt.Errorf("回调缺少 signature"))
	}
	if goallpaySign(values, cfg.Secret) != values.Get("signature") {
		return fail(fmt.Errorf("GoAllPay 回调签名验证失败"))
	}
	if values.Get("RespCode") != "00" {
		return fail(ErrTradeStatus)
	}
	cents, err := xunhuYuanToCents(values.Get("orderAmount"))
	if err != nil {
		return fail(err)
	}
	return NotifyResult{OK: true, TradeNo: values.Get("transID"), AmountCents: cents}
}

func (g GoallpayGateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}
