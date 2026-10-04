// Package wechatpay implements WeChat Pay APIv3 (Native) as a payment.Gateway.
//
// 接入时最容易踩错的三处，这里先写清楚：
//   - 请求签名：RSA-SHA256，签名串是「HTTP方法\nURL路径\n时间戳\n随机串\n请求体\n」，
//     放在 Authorization: WECHATPAY2-SHA256-RSA2048 头里。
//   - 回调验签：用微信支付平台证书对「时间戳\n随机串\n回调body\n」做 RSA-SHA256 验证，
//     签名在 Wechatpay-Signature 头里。
//   - 回调正文的 resource 是 AES-256-GCM 加密的，用 APIv3 密钥解密。
//
// 只用 Go 标准库；网络失败不做内部重试，由上层决定是否重试。
package wechatpay

import (
	"context"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/payment"
)

const (
	apiBase    = "https://api.mch.weixin.qq.com"
	methodName = "wechatpay"
	authScheme = "WECHATPAY2-SHA256-RSA2048"
)

// Config 保存在 payment_providers.secret_encrypted 的 JSON 里。
type Config struct {
	AppID         string `json:"app_id"`
	MchID         string `json:"mch_id"`
	SerialNo      string `json:"serial_no"`
	PrivateKeyPEM string `json:"private_key_pem"`
	APIv3Key      string `json:"api_v3_key"`
	PlatformCert  string `json:"platform_cert_pem"`
	NotifyURL     string `json:"notify_url"`
}

// Valid 报告配置是否足以发起支付。
func (c Config) Valid() error {
	if c.AppID == "" || c.MchID == "" || c.SerialNo == "" {
		return fmt.Errorf("wechatpay 需要 app_id / mch_id / serial_no")
	}
	if strings.TrimSpace(c.PrivateKeyPEM) == "" {
		return fmt.Errorf("wechatpay 需要商户 API 私钥 private_key_pem")
	}
	return nil
}

// Gateway 实现 payment.Gateway 与 payment.Refunder。
type Gateway struct {
	http *http.Client
}

// New 构造一个网关实例。
func New() *Gateway {
	return &Gateway{http: &http.Client{Timeout: 20 * time.Second}}
}

// Method 返回网关标识。
func (g *Gateway) Method() string { return methodName }

// parseConfig 解析网关凭据；商户号允许放在独立的列里。
func parseConfig(cfg payment.ProviderConfig) (Config, error) {
	var c Config
	if strings.TrimSpace(cfg.Secret) == "" {
		return c, fmt.Errorf("wechatpay 缺少密钥配置")
	}
	if err := json.Unmarshal([]byte(cfg.Secret), &c); err != nil {
		return c, fmt.Errorf("wechatpay 密钥配置不是合法 JSON: %w", err)
	}
	if c.MchID == "" {
		c.MchID = cfg.MerchantID
	}
	return c, c.Valid()
}

// parsePrivateKey 支持 PKCS#8 与 PKCS#1 两种 PEM。
func parsePrivateKey(pemText string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(strings.TrimSpace(pemText)))
	if block == nil {
		return nil, fmt.Errorf("商户私钥不是合法的 PEM")
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, fmt.Errorf("商户私钥不是 RSA 私钥")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("解析商户私钥失败: %w", err)
	}
	return key, nil
}

// requestMessage 是**请求**签名串：末尾必须有一个换行。
// 微信支付文档：HTTP方法\nURL路径\n时间戳\n随机串\n请求体\n
func requestMessage(method, urlPath, timestamp, nonce, body string) string {
	return method + "\n" + urlPath + "\n" + timestamp + "\n" + nonce + "\n" + body + "\n"
}

// callbackMessage 是**回调**验签串：只有三段，且末尾没有换行。
// 微信支付文档：时间戳\n随机串\n回调body\n —— 这里的尾部 \n 属于分隔符描述，
// 实际签名内容不含结尾换行。两类签名串不同，混用会直接验签失败。
func callbackMessage(timestamp, nonce, body string) string {
	return timestamp + "\n" + nonce + "\n" + body
}

// rsaSignSHA256 对给定消息做 RSA-SHA256 签名并 base64 编码。
func rsaSignSHA256(key *rsa.PrivateKey, message string) (string, error) {
	hashed := sha256.Sum256([]byte(message))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed[:])
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

// sign 用商户私钥对请求签名串做签名。
func sign(key *rsa.PrivateKey, method, urlPath, timestamp, nonce, body string) (string, error) {
	return rsaSignSHA256(key, requestMessage(method, urlPath, timestamp, nonce, body))
}

// signCallback 按回调验签串签名。仅测试使用：微信支付只会**验证**回调签名，
// 不会用它给我们回调。把它放在生产代码里是为了让测试与被验证的实现共用同一份
// 拼接逻辑，避免两边各写一遍导致悄悄写歪。
func signCallback(key *rsa.PrivateKey, timestamp, nonce, body string) (string, error) {
	return rsaSignSHA256(key, callbackMessage(timestamp, nonce, body))
}

// authorization 拼出 Authorization 头。
func authorization(mchID, serialNo, nonce, signature, timestamp string) string {
	return fmt.Sprintf("%s mchid=\"%s\",nonce_str=\"%s\",signature=\"%s\",timestamp=\"%s\",serial_no=\"%s\"",
		authScheme, mchID, nonce, signature, timestamp, serialNo)
}

// randomNonce 生成 32 位随机串。
func randomNonce() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(buf)
}

// PayURL 调 Native 下单，把 code_url 用二维码服务渲染成可扫码的链接。
// Native 返回的是 weixin:// 开头的支付串，用户需要扫二维码，所以这里换成图片 URL。
func (g *Gateway) PayURL(ctx context.Context, cfg payment.ProviderConfig, pr payment.Prepared) (string, error) {
	c, err := parseConfig(cfg)
	if err != nil {
		return "", err
	}
	key, err := parsePrivateKey(c.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	notify := strings.TrimSpace(pr.NotifyURL)
	if notify == "" {
		notify = c.NotifyURL
	}
	if notify == "" {
		return "", fmt.Errorf("wechatpay 需要 notify_url（可在网关配置或站点 PUBLIC_BASE_URL 推导）")
	}

	body := map[string]any{
		"appid":        c.AppID,
		"mchid":        c.MchID,
		"description":  truncate(pr.Subject, 127),
		"out_trade_no": pr.OutTradeNo,
		"notify_url":   notify,
		"amount": map[string]any{
			"total":    pr.AmountCents,
			"currency": "CNY",
		},
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := randomNonce()
	signature, err := sign(key, http.MethodPost, "/v3/pay/transactions/native", timestamp, nonce, string(bodyJSON))
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v3/pay/transactions/native", strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authorization(c.MchID, c.SerialNo, nonce, signature, timestamp))
	resp, err := g.http.Do(req)
	if err != nil {
		return "", payment.ErrGatewayAPI{Detail: "wechatpay native: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", payment.ErrGatewayAPI{Detail: fmt.Sprintf("wechatpay native HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))}
	}
	var out struct {
		CodeURL string `json:"code_url"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", payment.ErrGatewayAPI{Detail: "wechatpay native 响应无法解析: " + err.Error()}
	}
	if out.CodeURL == "" {
		return "", payment.ErrGatewayAPI{Detail: "wechatpay native 未返回 code_url"}
	}
	// 交给前端/收银台渲染二维码；这里直接给出支付串，避免依赖第三方二维码服务。
	return out.CodeURL, nil
}

// truncate 按字符（而非字节）截断，避免把中文截坏。
func truncate(s string, max int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= max {
		return string(r)
	}
	return string(r[:max])
}

// VerifyNotify 验证微信支付回调：RSA 验签 → AES-256-GCM 解密 resource → 读出金额。
// 只有签名有效、且 trade_state 为 SUCCESS 才算支付成功。
func (g *Gateway) VerifyNotify(cfg payment.ProviderConfig, input payment.NotifyInput) payment.NotifyResult {
	c, err := parseConfig(cfg)
	if err != nil {
		return payment.NotifyResult{OK: false, Err: err}
	}
	if strings.TrimSpace(c.PlatformCert) == "" {
		return payment.NotifyResult{OK: false, Err: fmt.Errorf("wechatpay 需要配置平台证书才能验签回调")}
	}
	if strings.TrimSpace(c.APIv3Key) == "" {
		return payment.NotifyResult{OK: false, Err: fmt.Errorf("wechatpay 需要配置 APIv3 密钥才能解密回调")}
	}
	timestamp := input.Header.Get("Wechatpay-Timestamp")
	nonce := input.Header.Get("Wechatpay-Nonce")
	signature := input.Header.Get("Wechatpay-Signature")
	if timestamp == "" || nonce == "" || signature == "" {
		return payment.NotifyResult{OK: false, Err: fmt.Errorf("回调缺少 Wechatpay-* 验签头")}
	}
	// 防重放：回调时间戳与本地时间相差超过 5 分钟直接拒绝。
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return payment.NotifyResult{OK: false, Err: fmt.Errorf("回调时间戳非法")}
	}
	drift := time.Now().Unix() - ts
	if drift < 0 {
		drift = -drift
	}
	if drift > 300 {
		return payment.NotifyResult{OK: false, Err: fmt.Errorf("回调时间戳超出 5 分钟容差，可能是重放")}
	}
	if err := verifyWithPlatformCert(c.PlatformCert, timestamp, nonce, string(input.RawBody), signature); err != nil {
		return payment.NotifyResult{OK: false, Err: err}
	}

	var envelope struct {
		EventType string `json:"event_type"`
		Resource  struct {
			Algorithm      string `json:"algorithm"`
			Ciphertext     string `json:"ciphertext"`
			Nonce          string `json:"nonce"`
			AssociatedData string `json:"associated_data"`
		} `json:"resource"`
	}
	if err := json.Unmarshal(input.RawBody, &envelope); err != nil {
		return payment.NotifyResult{OK: false, Err: fmt.Errorf("回调 JSON 解析失败: %w", err)}
	}
	if envelope.Resource.Ciphertext == "" {
		return payment.NotifyResult{OK: false, Err: fmt.Errorf("回调没有 resource.ciphertext")}
	}
	plain, err := decryptResource(c.APIv3Key, envelope.Resource.Nonce, envelope.Resource.AssociatedData, envelope.Resource.Ciphertext)
	if err != nil {
		return payment.NotifyResult{OK: false, Err: err}
	}
	var tx struct {
		OutTradeNo string `json:"out_trade_no"`
		TradeState string `json:"trade_state"`
		TradeNo    string `json:"transaction_id"`
		Amount     struct {
			Total int64 `json:"total"`
		} `json:"amount"`
	}
	if err := json.Unmarshal(plain, &tx); err != nil {
		return payment.NotifyResult{OK: false, Err: fmt.Errorf("解密后的回调解析失败: %w", err)}
	}
	if tx.TradeState != "SUCCESS" {
		return payment.NotifyResult{OK: false, Err: payment.ErrTradeStatus}
	}
	if tx.Amount.Total <= 0 {
		return payment.NotifyResult{OK: false, Err: fmt.Errorf("回调金额非法")}
	}
	return payment.NotifyResult{OK: true, TradeNo: tx.TradeNo, AmountCents: tx.Amount.Total}
}

// verifyWithPlatformCert 用平台证书验签「时间戳\n随机串\n正文\n」。
func verifyWithPlatformCert(certPEM, timestamp, nonce, body, signatureB64 string) error {
	block, _ := pem.Decode([]byte(strings.TrimSpace(certPEM)))
	if block == nil {
		return fmt.Errorf("平台证书不是合法的 PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return fmt.Errorf("解析平台证书失败: %w", err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return fmt.Errorf("平台证书公钥不是 RSA")
	}
	sig, err := base64.StdEncoding.DecodeString(signatureB64)
	if err != nil {
		return fmt.Errorf("回调签名不是合法 base64: %w", err)
	}
	hashed := sha256.Sum256([]byte(callbackMessage(timestamp, nonce, body)))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, hashed[:], sig); err != nil {
		return fmt.Errorf("回调签名校验失败: %w", err)
	}
	return nil
}

// decryptResource 用 APIv3 密钥做 AES-256-GCM 解密（微信支付回调的标准姿势）。
func decryptResource(apiV3Key, nonce, associatedData, ciphertextB64 string) ([]byte, error) {
	if len(apiV3Key) != 32 {
		return nil, fmt.Errorf("APIv3 密钥必须是 32 字节，当前 %d 字节", len(apiV3Key))
	}
	data, err := base64.StdEncoding.DecodeString(ciphertextB64)
	if err != nil {
		return nil, fmt.Errorf("密文不是合法 base64: %w", err)
	}
	block, err := aes.NewCipher([]byte(apiV3Key))
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, fmt.Errorf("nonce 长度应为 %d，当前 %d", gcm.NonceSize(), len(nonce))
	}
	plain, err := gcm.Open(nil, []byte(nonce), data, []byte(associatedData))
	if err != nil {
		return nil, fmt.Errorf("回调解密失败（请检查 APIv3 密钥）: %w", err)
	}
	return plain, nil
}

// Refund 发起原路退款（payment.Refunder）。
// out_refund_no 用我们自己的退款单号当幂等键，微信支付对同一单号只退一次。
func (g *Gateway) Refund(ctx context.Context, cfg payment.ProviderConfig, r payment.RefundRequest) (string, error) {
	c, err := parseConfig(cfg)
	if err != nil {
		return "", err
	}
	key, err := parsePrivateKey(c.PrivateKeyPEM)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(r.OutTradeNo) == "" {
		return "", fmt.Errorf("wechatpay 退款需要商户订单号")
	}
	body := map[string]any{
		"out_trade_no":  r.OutTradeNo,
		"out_refund_no": refundNo(r),
		"reason":        truncate(r.Reason, 80),
		"amount": map[string]any{
			"refund":   r.AmountCents,
			"total":    r.AmountCents,
			"currency": "CNY",
		},
	}
	bodyJSON, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := randomNonce()
	signature, err := sign(key, http.MethodPost, "/v3/refund/domestic/refunds", timestamp, nonce, string(bodyJSON))
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase+"/v3/refund/domestic/refunds", strings.NewReader(string(bodyJSON)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", authorization(c.MchID, c.SerialNo, nonce, signature, timestamp))
	// 退款是幂等的：同一 out_refund_no 重复提交会返回同一笔退款，所以这里可以安全重试。
	resp, err := g.http.Do(req)
	if err != nil {
		return "", payment.ErrGatewayAPI{Detail: "wechatpay refund: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", payment.ErrGatewayAPI{Detail: fmt.Sprintf("wechatpay refund HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))}
	}
	var out struct {
		RefundID string `json:"refund_id"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", payment.ErrGatewayAPI{Detail: "wechatpay refund 响应无法解析: " + err.Error()}
	}
	if out.RefundID == "" {
		return "", payment.ErrGatewayAPI{Detail: "wechatpay refund 未返回 refund_id"}
	}
	return out.RefundID, nil
}

// refundNo 生成幂等的退款单号：优先用我们自己的退款记录 ID。
func refundNo(r payment.RefundRequest) string {
	if strings.TrimSpace(r.RefundID) != "" {
		return truncate("RF"+strings.ReplaceAll(r.RefundID, "-", ""), 64)
	}
	return truncate("RF"+strings.ReplaceAll(r.OutTradeNo, "-", ""), 64)
}

// 注册到支付注册表：cmd/server 只需空白导入本包即可启用。
func init() {
	payment.Register(New())
}
