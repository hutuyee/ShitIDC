package payment

import (
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/alipaykit"
)

// AlipayGateway implements 支付宝电脑网站支付 (alipay.trade.page.pay, RSA2).
//
// Provider fields:
//   - MerchantID: 支付宝 openplatform 应用 APPID
//   - GatewayURL: https://openapi.alipay.com/gateway.do （或沙箱网关）
//   - Secret:     JSON {"merchant_private_key":"PKCS8 PEM 或纯 base64",
//     "alipay_public_key":"X.509 PEM 或纯 base64"}
type AlipayGateway struct{}

func (AlipayGateway) Method() string { return "alipay" }

type alipaySecrets struct {
	MerchantPrivateKey string `json:"merchant_private_key"`
	AlipayPublicKey    string `json:"alipay_public_key"`
}

func parseAlipaySecret(raw string) (alipaySecrets, error) {
	var s alipaySecrets
	if err := json.Unmarshal([]byte(raw), &s); err != nil || s.MerchantPrivateKey == "" {
		return alipaySecrets{}, errors.New("alipay 密钥需为 JSON {\"merchant_private_key\",\"alipay_public_key\"}")
	}
	return s, nil
}

// parsePrivateKey accepts PKCS8/PKCS1 PEM or a bare base64 (PKCS8) body.
// 实现在 internal/alipaykit，与第三方登录共用一份。
func parsePrivateKey(raw string) (*rsa.PrivateKey, error) { return alipaykit.ParsePrivateKey(raw) }

// parsePublicKey accepts X.509 PEM or a bare base64 SubjectPublicKeyInfo.
func parsePublicKey(raw string) (*rsa.PublicKey, error) { return alipaykit.ParsePublicKey(raw) }

// alipayMoney renders cents as the decimal string Alipay expects ("12.30").
func alipayMoney(cents int64) string {
	return strconv.FormatInt(cents/100, 10) + "." + fmt.Sprintf("%02d", cents%100)
}

// PayURL builds the alipay.trade.page.pay redirect URL with an RSA2 signature.
func (g AlipayGateway) PayURL(_ context.Context, cfg ProviderConfig, p Prepared) (string, error) {
	s, err := parseAlipaySecret(cfg.Secret)
	if err != nil {
		return "", err
	}
	if cfg.MerchantID == "" {
		return "", errors.New("alipay APPID（商户 ID）不能为空")
	}
	if p.AmountCents <= 0 {
		return "", errors.New("支付金额必须大于 0")
	}
	privateKey, err := parsePrivateKey(s.MerchantPrivateKey)
	if err != nil {
		return "", err
	}
	biz, _ := json.Marshal(map[string]string{
		"out_trade_no": p.OutTradeNo,
		"total_amount": alipayMoney(p.AmountCents),
		"subject":      p.Subject,
		"product_code": "FAST_INSTANT_TRADE_PAY",
	})
	params := url.Values{
		"app_id":      {cfg.MerchantID},
		"method":      {"alipay.trade.page.pay"},
		"format":      {"JSON"},
		"charset":     {"utf-8"},
		"sign_type":   {"RSA2"},
		"timestamp":   {time.Now().Format("2006-01-02 15:04:05")},
		"version":     {"1.0"},
		"notify_url":  {p.NotifyURL},
		"return_url":  {p.ReturnURL},
		"biz_content": {string(biz)},
	}
	content := alipaykit.SignContent(params)
	sign, err := alipaykit.SignRSA2(privateKey, content)
	if err != nil {
		return "", errors.New("支付宝签名失败: " + err.Error())
	}
	params.Set("sign", sign)
	base := strings.TrimRight(cfg.GatewayURL, "/")
	if base == "" {
		base = "https://openapi.alipay.com/gateway.do"
	}
	return base + "?" + url.Values{"sign": {sign}, "sign_type": {"RSA2"}}.Encode() +
		"&" + alipayQueryNoSign(params), nil
}

// alipayQueryNoSign renders params (already ordered by url.Values.Encode on
// the caller side requirement) without sign/sign_type.
func alipayQueryNoSign(params url.Values) string {
	q := url.Values{}
	for k, vs := range params {
		if k == "sign" || k == "sign_type" {
			continue
		}
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return q.Encode()
}

// VerifyNotify validates an async notify (POST form) or return (GET query)
// callback: RSA2 signature, trade_status and amount.
func (g AlipayGateway) VerifyNotify(cfg ProviderConfig, input NotifyInput) NotifyResult {
	fail := func(err error) NotifyResult { return NotifyResult{OK: false, Err: err} }
	s, err := parseAlipaySecret(cfg.Secret)
	if err != nil {
		return fail(err)
	}
	if s.AlipayPublicKey == "" {
		return fail(errors.New("未配置支付宝公钥，无法验证回调"))
	}
	publicKey, err := parsePublicKey(s.AlipayPublicKey)
	if err != nil {
		return fail(err)
	}
	values := input.Values()
	sign := values.Get("sign")
	if sign == "" {
		return fail(errors.New("回调缺少 sign"))
	}
	if !alipaykit.VerifyRSA2(publicKey, alipaykit.SignContent(values), sign) {
		return fail(errors.New("支付宝回调签名验证失败"))
	}
	status := values.Get("trade_status")
	if status != "TRADE_SUCCESS" && status != "TRADE_FINISHED" {
		return fail(fmt.Errorf("trade_status %q 不代表支付成功", status))
	}
	cents, err := alipayYuanToCents(values.Get("total_amount"))
	if err != nil {
		return fail(err)
	}
	return NotifyResult{OK: true, TradeNo: values.Get("trade_no"), AmountCents: cents}
}

func alipayYuanToCents(v string) (int64, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, errors.New("金额为空")
	}
	parts := strings.SplitN(v, ".", 2)
	yuan, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, errors.New("金额格式无效")
	}
	cents := yuan * 100
	if len(parts) == 2 {
		frac := parts[1]
		for len(frac) < 2 {
			frac += "0"
		}
		f, err := strconv.Atoi(frac[:2])
		if err != nil {
			return 0, errors.New("金额格式无效")
		}
		cents += int64(f)
	}
	return cents, nil
}

// Refund issues alipay.trade.refund (自动退款), idempotent via out_request_no.
func (g AlipayGateway) Refund(ctx context.Context, cfg ProviderConfig, r RefundRequest) (string, error) {
	s, err := parseAlipaySecret(cfg.Secret)
	if err != nil {
		return "", err
	}
	privateKey, err := parsePrivateKey(s.MerchantPrivateKey)
	if err != nil {
		return "", err
	}
	outRequestNo := r.RefundID
	if outRequestNo == "" {
		outRequestNo = r.OutTradeNo + "-r"
	}
	biz, _ := json.Marshal(map[string]string{
		"out_trade_no":   r.OutTradeNo,
		"refund_amount":  alipayMoney(r.AmountCents),
		"out_request_no": outRequestNo,
		"refund_reason":  firstNonEmpty(r.Reason, "order refund"),
	})
	params := url.Values{
		"app_id":      {cfg.MerchantID},
		"method":      {"alipay.trade.refund"},
		"format":      {"JSON"},
		"charset":     {"utf-8"},
		"sign_type":   {"RSA2"},
		"timestamp":   {time.Now().Format("2006-01-02 15:04:05")},
		"version":     {"1.0"},
		"biz_content": {string(biz)},
	}
	sign, err := alipaykit.SignRSA2(privateKey, alipaykit.SignContent(params))
	if err != nil {
		return "", errors.New("支付宝退款签名失败: " + err.Error())
	}
	params.Set("sign", sign)
	base := strings.TrimRight(cfg.GatewayURL, "/")
	if base == "" {
		base = "https://openapi.alipay.com/gateway.do"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, strings.NewReader(params.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", ErrGatewayAPI{Detail: err.Error()}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		Code       string `json:"code"`
		Msg        string `json:"msg"`
		SubMsg     string `json:"sub_msg"`
		FundChange string `json:"fund_change"`
		TradeNo    string `json:"trade_no"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", ErrGatewayAPI{Detail: "decode refund response: " + truncate(body, 200)}
	}
	if out.Code != "10000" {
		return "", ErrGatewayAPI{Detail: fmt.Sprintf("alipay refund %s %s %s", out.Code, out.Msg, out.SubMsg)}
	}
	if out.FundChange != "Y" {
		return "", ErrGatewayAPI{Detail: "alipay refund fund_change != Y"}
	}
	return firstNonEmpty(out.TradeNo, outRequestNo), nil
}

func init() {
	Register(AlipayGateway{})
}
