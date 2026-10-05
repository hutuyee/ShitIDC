// Package payment —— OcgcPay（OCGC / 酷云 koolyun 扫码支付）网关。
//
// 对应魔方 public/plugins/gateways/OcgcPay。协议分两步，全程 RSA 签名：
//
//  1. 登录  POST {api_url}  body: params=[{action:"msc/user/login",...}]
//     请求头 x-appkey(=merchId) 与 x-apsignature；响应头 x-apsessionid
//     取会话，响应体带 batchNo / stl_cur（结算币种）。
//  2. 下单  POST {api_url}  body: params=[{action:"msc/txn/request",...}]
//     附加 x-apsessionid；响应体 data 字段里是 {qrcodeResult,qrcodeUrl}。
//
// 签名细节（与参考实现 vendor/QrcodePay.Data.php 逐字节对齐）：
//   - 请求体是 **JSON 数组包裹单个对象**，且对象字段必须保持参考实现里
//     的**声明顺序**（签名覆盖原始字节，字段顺序错了签名就对不上）；
//     未赋值的字段以空字符串出现——Go 里用固定顺序的 struct 序列化保证。
//   - x-apsignature = 大写 hex(RSA 签名(JSON 字符串))。PHP openssl_sign 不传
//     算法参数时默认 SHA1，参考实现正是这么调的——这里同样用 SHA1，
//     与线上魔方插件互通；如需切换 SHA256 只改摘要类型一处。
//   - 响应体外层有 [...]，签名覆盖**含方括号的原始字节**。
package payment

import (
	"context"
	"crypto"
	"crypto/md5"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// OcgcPayGateway 实现酷云扫码支付。
type OcgcPayGateway struct {
	// Now 允许测试注入。
	Now func() time.Time
}

// Method 返回网关标识。
func (OcgcPayGateway) Method() string { return "ocgcpay" }

func init() { Register(OcgcPayGateway{}) }

type ocgcSecrets struct {
	PrivateKey string `json:"private_key"`
	PublicKey  string `json:"public_key"`
	LoginPwd   string `json:"login_pwd"`
}

func parseOcgcSecret(raw string) (ocgcSecrets, error) {
	var s ocgcSecrets
	if err := json.Unmarshal([]byte(raw), &s); err != nil || s.PrivateKey == "" {
		return ocgcSecrets{}, fmt.Errorf("ocgcpay 密钥需为 JSON {\"private_key\",\"public_key\",\"login_pwd\"}")
	}
	return s, nil
}

// ocgcPrivateKey 把裸 base64（PKCS1）或 PEM 的私钥解析出来。
func ocgcPrivateKey(raw string) (*rsa.PrivateKey, error) {
	raw = strings.TrimSpace(raw)
	if block, _ := pem.Decode([]byte(raw)); block != nil {
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	}
	// 参考实现按 64 字符折行补 PEM 头尾（密钥文件是 base64），这里一致。
	wrapped := "-----BEGIN RSA PRIVATE KEY-----\n"
	for i := 0; i < len(raw); i += 64 {
		end := i + 64
		if end > len(raw) {
			end = len(raw)
		}
		wrapped += raw[i:end] + "\n"
	}
	wrapped += "-----END RSA PRIVATE KEY-----"
	block, _ := pem.Decode([]byte(wrapped))
	if block == nil {
		return nil, fmt.Errorf("酷云商户私钥解析失败")
	}
	return x509.ParsePKCS1PrivateKey(block.Bytes)
}

// ocgcPublicKey 解析酷云公钥（裸 base64 X.509/PKIX 或 PEM）。
func ocgcPublicKey(raw string) (*rsa.PublicKey, error) {
	raw = strings.TrimSpace(raw)
	if block, _ := pem.Decode([]byte(raw)); block != nil {
		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		key, ok := pub.(*rsa.PublicKey)
		if !ok {
			return nil, fmt.Errorf("酷云公钥不是 RSA 公钥")
		}
		return key, nil
	}
	wrapped := "-----BEGIN PUBLIC KEY-----\n"
	for i := 0; i < len(raw); i += 64 {
		end := i + 64
		if end > len(raw) {
			end = len(raw)
		}
		wrapped += raw[i:end] + "\n"
	}
	wrapped += "-----END PUBLIC KEY-----"
	block, _ := pem.Decode([]byte(wrapped))
	if block == nil {
		return nil, fmt.Errorf("酷云公钥解析失败")
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := pub.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("酷云公钥不是 RSA 公钥")
	}
	return key, nil
}

// ocgcSign 上大写 hex(RSA-SHA1)。与 PHP openssl_sign 默认算法一致。
func ocgcSign(key *rsa.PrivateKey, data string) (string, error) {
	digest := sha1.Sum([]byte(data))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA1, digest[:])
	if err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(sig)), nil
}

// ocgcVerify 验证酷云侧签名。
func ocgcVerify(key *rsa.PublicKey, data, signHex string) bool {
	sig, err := hex.DecodeString(strings.TrimSpace(signHex))
	if err != nil {
		return false
	}
	digest := sha1.Sum([]byte(data))
	return rsa.VerifyPKCS1v15(key, crypto.SHA1, digest[:], sig) == nil
}

// loginRequest / mscTxnRequest 的字段顺序必须与参考 PHP 类的属性声明一致。
type ocgcLoginRequest struct {
	Action   string `json:"action"`
	V        string `json:"v"`
	IposSn   string `json:"iposSn"`
	MerchID  string `json:"merchId"`
	Operator string `json:"operator"`
	Pwd      string `json:"pwd"`
}

type ocgcTxnRequest struct {
	Action         string `json:"action"`
	PaymentID      string `json:"paymentId"`
	TransType      string `json:"transType"`
	BatchNo        string `json:"batchNo"`
	TraceNo        string `json:"traceNo"`
	TransTime      string `json:"transTime"`
	TransAmount    int64  `json:"transAmount"`
	OdNo           string `json:"odNo"`
	OdDesc         string `json:"odDesc"`
	Currency       string `json:"currency"`
	NotifyURL      string `json:"notifyUrl"`
	TransTimeOut   string `json:"transTimeOut"`
	Data           string `json:"data"`
	DataPayType    string `json:"dataPayType"`
	DataAction     string `json:"dataAction"`
	DataQrcodeType string `json:"dataQrcodeType"`
	DataPayCode    string `json:"dataPayCode"`
	DataWalletType string `json:"dataWalletType"`
	DataReturnURL  string `json:"dataReturnUrl"`
	DataRefNo      string `json:"dataRefNo"`
	DataOriTxnID   string `json:"dataOriTxnId"`
	DataOpenID     string `json:"dataOpenId"`
	DataSubAppID   string `json:"dataSubAppId"`
	DataSubOpenID  string `json:"dataSubOpenId"`
}

// ocgcPost 发一个 params=[json] 请求，返回 (会话ID, 响应体对象, 错误)。
func (g OcgcPayGateway) ocgcPost(ctx context.Context, cfg ProviderConfig, key *rsa.PrivateKey, pub *rsa.PublicKey, payload any, session string) (string, map[string]any, error) {
	bodyJSON, err := json.Marshal(payload)
	if err != nil {
		return "", nil, err
	}
	// object2json 包裹成单元素数组。
	wrapped := "[" + string(bodyJSON) + "]"
	sig, err := ocgcSign(key, wrapped)
	if err != nil {
		return "", nil, err
	}
	base := strings.TrimRight(cfg.GatewayURL, "/")
	if base == "" {
		base = "https://aop.koolyun.com:443/apmp/rest"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base, strings.NewReader("params="+wrapped))
	if err != nil {
		return "", nil, err
	}
	// 注意：参考实现原样拼接 params=JSON，不做 URL 编码。
	req.Header.Set("X-APVersion", "1.0")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	req.Header.Set("Accept-Language", "zh-CN")
	req.Header.Set("X-APFormat", "json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("x-appkey", cfg.MerchantID)
	req.Header.Set("x-apsignature", sig)
	if session != "" {
		req.Header.Set("x-apsessionid", session)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, ErrGatewayAPI{Detail: "酷云请求失败: " + err.Error()}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", nil, err
	}
	body := strings.TrimSpace(string(raw))
	// 响应验签覆盖含方括号的原始字节（与参考实现一致）。
	if respSig := strings.TrimSpace(resp.Header.Get("x-apsignature")); respSig != "" && pub != nil {
		if !ocgcVerify(pub, body, respSig) {
			return "", nil, ErrGatewayAPI{Detail: "酷云响应签名验证失败"}
		}
	}
	newSession := strings.TrimSpace(resp.Header.Get("x-apsessionid"))
	inner := strings.TrimSuffix(strings.TrimPrefix(body, "["), "]")
	var parsed map[string]any
	if err := json.Unmarshal([]byte(inner), &parsed); err != nil {
		return "", nil, ErrGatewayAPI{Detail: fmt.Sprintf("酷云返回无法解析(HTTP %d): %s", resp.StatusCode, truncatePayLog(raw))}
	}
	return newSession, parsed, nil
}

// PayURL 登录 + 下单，返回支付页地址。
// 参考实现把 qrcodeUrl 嵌进 <object> 展示；本系统的支付流程是整页跳转，
// 所以只接受 http(s) 的地址，二维码图 URL 无法承载时明确报错。
func (g OcgcPayGateway) PayURL(ctx context.Context, cfg ProviderConfig, p Prepared) (string, error) {
	if cfg.MerchantID == "" {
		return "", fmt.Errorf("ocgcpay 需要 merchant_id（merchId）")
	}
	secret, err := parseOcgcSecret(cfg.Secret)
	if err != nil {
		return "", err
	}
	if p.AmountCents <= 0 {
		return "", fmt.Errorf("支付金额必须大于 0")
	}
	key, err := ocgcPrivateKey(secret.PrivateKey)
	if err != nil {
		return "", err
	}
	var pub *rsa.PublicKey
	if secret.PublicKey != "" {
		pub, _ = ocgcPublicKey(secret.PublicKey)
	}
	now := g.now()

	login := ocgcLoginRequest{
		Action:   "msc/user/login",
		V:        "3.0",
		IposSn:   "0000000000000000",
		MerchID:  cfg.MerchantID,
		Operator: "01",
		Pwd:      md5Hex(secret.LoginPwd),
	}
	_, loginResp, err := g.ocgcPost(ctx, cfg, key, pub, login, "")
	if err != nil {
		return "", err
	}
	batchNo := scalarToString(loginResp["batchNo"])
	currency := scalarToString(loginResp["stl_cur"])
	if batchNo == "" {
		return "", ErrGatewayAPI{Detail: "酷云登录失败：" + firstNonEmptyPay(scalarToString(loginResp["errorMsg"]), "未返回 batchNo")}
	}

	dataPayload, _ := json.Marshal(map[string]string{
		"payType":    "alipayOverSeaOnline",
		"action":     "csbpay",
		"qrcodeType": "ALIPAYCN",
	})
	txn := ocgcTxnRequest{
		Action:      "msc/txn/request",
		PaymentID:   "0000000392",
		TransType:   "1021",
		BatchNo:     batchNo,
		TraceNo:     "000002",
		TransTime:   now.Format("20060102150405"),
		TransAmount: p.AmountCents,
		OdNo:        p.OutTradeNo,
		OdDesc:      p.Subject,
		Currency:    currency,
		NotifyURL:   p.NotifyURL,
		Data:        string(dataPayload),
		DataPayType: "alipayOverSeaOnline",
		DataAction:  "csbpay",
	}
	_, txnResp, err := g.ocgcPost(ctx, cfg, key, pub, txn, "")
	if err != nil {
		return "", err
	}
	dataStr := scalarToString(txnResp["data"])
	if dataStr == "" {
		return "", ErrGatewayAPI{Detail: "酷云下单失败：" + firstNonEmptyPay(scalarToString(txnResp["errorMsg"]), "响应缺少 data")}
	}
	var inner struct {
		QrcodeResult string `json:"qrcodeResult"`
		QrcodeURL    string `json:"qrcodeUrl"`
		ResultMsg    string `json:"resultMsg"`
	}
	if err := json.Unmarshal([]byte(dataStr), &inner); err != nil {
		return "", ErrGatewayAPI{Detail: "酷云下单 data 无法解析: " + truncatePayLog([]byte(dataStr))}
	}
	if !strings.EqualFold(inner.QrcodeResult, "SUCCESS") || inner.QrcodeURL == "" {
		return "", ErrGatewayAPI{Detail: "酷云下单失败：" + firstNonEmptyPay(inner.ResultMsg, inner.QrcodeResult, "未知错误")}
	}
	if !strings.HasPrefix(inner.QrcodeURL, "http://") && !strings.HasPrefix(inner.QrcodeURL, "https://") {
		return "", ErrGatewayAPI{Detail: "酷云返回的是二维码地址而非跳转地址，当前支付流程无法展示：" + truncatePayLog([]byte(inner.QrcodeURL))}
	}
	return inner.QrcodeURL, nil
}

// VerifyNotify 校验异步回调：RSA 验签 body，body 里带 odNo / amount(分) / txnId。
func (g OcgcPayGateway) VerifyNotify(cfg ProviderConfig, input NotifyInput) NotifyResult {
	fail := func(err error) NotifyResult { return NotifyResult{OK: false, Err: err} }
	secret, err := parseOcgcSecret(cfg.Secret)
	if err != nil {
		return fail(err)
	}
	if secret.PublicKey == "" {
		return fail(fmt.Errorf("未配置酷云公钥，无法验证回调"))
	}
	pub, err := ocgcPublicKey(secret.PublicKey)
	if err != nil {
		return fail(err)
	}
	body := input.Values().Get("body")
	signHex := input.Values().Get("sign")
	if body == "" || signHex == "" {
		return fail(fmt.Errorf("回调缺少 body 或 sign"))
	}
	if !ocgcVerify(pub, body, signHex) {
		return fail(fmt.Errorf("酷云回调签名验证失败"))
	}
	var payload struct {
		OdNo   string          `json:"odNo"`
		Amount json.RawMessage `json:"amount"`
		TxnID  string          `json:"txnId"`
	}
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return fail(fmt.Errorf("酷云回调 body 无法解析"))
	}
	amount, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(string(payload.Amount)), `"`), 10, 64)
	if err != nil {
		return fail(fmt.Errorf("酷云回调金额无效"))
	}
	return NotifyResult{OK: true, TradeNo: payload.TxnID, AmountCents: amount}
}

func (g OcgcPayGateway) now() time.Time {
	if g.Now != nil {
		return g.Now()
	}
	return time.Now()
}

func md5Hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}
