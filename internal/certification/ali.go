// 支付宝实名认证（对应魔方 public/plugins/certification/ali，芝麻信用）。
//
// 三步流程（全部请求走 openapi 网关，RSA2 签名）：
//  1. alipay.user.certify.open.initialize → certify_id
//  2. alipay.user.certify.open.certify    → 组装认证跳转地址（二维码内容）
//  3. alipay.user.certify.open.query      → passed 为 T/F
//
// 与魔方插件一致：网关默认 https://openapi.alipay.com/gateway.do、biz_code 默认
// SMART_FACE、biz_content 里带 merchant_config.return_url（可选）。
//
// 安全差异：插件的 AopClient 会校验响应签名；这里同样用支付宝公钥验签响应原文
// （sign 是对响应节点的原始 JSON 串签名），验不过直接报错——否则伪造的响应就能
// 把账号刷成「已认证」。
package certification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/alipaykit"
)

// Ali 实现支付宝实名认证通道。
type Ali struct {
	// Endpoint 可覆盖（测试指向本地假网关）。
	Endpoint string
	// Now 可注入时间（签名断言用）；为空则用 time.Now。
	Now  func() time.Time
	http *http.Client
}

// NewAli 构造通道。
func NewAli() *Ali {
	return &Ali{http: &http.Client{Timeout: 20 * time.Second}}
}

// Name 返回通道标识（沿用魔方插件标识）。
func (a *Ali) Name() string { return "ali" }

func init() { Register(NewAli()) }

// Validate 检查必填配置与凭据。
func (a *Ali) Validate(cfg Config, secret Secret) error {
	if cfg.Field("app_id") == "" {
		return fmt.Errorf("支付宝实名认证需要 app_id")
	}
	if secret.Get("private_key") == "" {
		return fmt.Errorf("支付宝实名认证需要应用私钥 private_key")
	}
	if secret.Get("alipay_public_key") == "" {
		return fmt.Errorf("支付宝实名认证需要支付宝公钥 alipay_public_key（响应验签用）")
	}
	return nil
}

// gateway 返回网关地址。
func (a *Ali) gateway(cfg Config) string {
	if a.Endpoint != "" {
		return a.Endpoint
	}
	if v := cfg.Field("gateway_url"); v != "" {
		return v
	}
	return "https://openapi.alipay.com/gateway.do"
}

func (a *Ali) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// bizCode 返回认证场景码。
func (a *Ali) bizCode(cfg Config) string {
	if v := cfg.Field("biz_code"); v != "" {
		return v
	}
	return "SMART_FACE"
}

// signParams 给公共参数 + biz_content 做 RSA2 签名，凑齐一次网关请求的参数。
func (a *Ali) signParams(cfg Config, secret Secret, method, bizContent string) (url.Values, error) {
	privateKey, err := alipaykit.ParsePrivateKey(secret.Get("private_key"))
	if err != nil {
		return nil, fmt.Errorf("解析应用私钥失败: %w", err)
	}
	params := url.Values{}
	params.Set("app_id", cfg.Field("app_id"))
	params.Set("method", method)
	params.Set("format", "json")
	params.Set("charset", "utf-8")
	params.Set("sign_type", "RSA2")
	params.Set("timestamp", a.now().Format("2006-01-02 15:04:05"))
	params.Set("version", "1.0")
	params.Set("biz_content", bizContent)
	sign, err := alipaykit.SignRSA2(privateKey, alipaykit.SignContent(params))
	if err != nil {
		return nil, fmt.Errorf("支付宝签名失败: %w", err)
	}
	params.Set("sign", sign)
	return params, nil
}

// call 执行一次网关调用并验签响应，返回响应节点的原始 JSON。
func (a *Ali) call(ctx context.Context, cfg Config, secret Secret, method, bizContent string) (json.RawMessage, error) {
	params, err := a.signParams(cfg, secret, method, bizContent)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.gateway(cfg), strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded;charset=utf-8")
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("支付宝请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("支付宝网关 HTTP %d：%s", resp.StatusCode, truncateCertLog(body))
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("支付宝响应无法解析: %s", truncateCertLog(body))
	}
	nodeName := strings.ReplaceAll(method, ".", "_") + "_response"
	rawNode, ok := top[nodeName]
	if !ok {
		return nil, fmt.Errorf("支付宝响应缺少 %s 节点: %s", nodeName, truncateCertLog(body))
	}
	pub, err := alipaykit.ParsePublicKey(secret.Get("alipay_public_key"))
	if err != nil {
		return nil, fmt.Errorf("解析支付宝公钥失败: %w", err)
	}
	var signWrapper struct {
		Sign string `json:"sign"`
	}
	_ = json.Unmarshal(body, &signWrapper)
	if signWrapper.Sign == "" {
		return nil, fmt.Errorf("支付宝响应缺少签名，拒绝采信")
	}
	if !alipaykit.VerifyRSA2(pub, string(rawNode), signWrapper.Sign) {
		return nil, fmt.Errorf("支付宝响应验签失败，拒绝采信")
	}
	return rawNode, nil
}

// aliNode 是各接口响应节点的公共字段。
type aliNode struct {
	Code      string `json:"code"`
	Msg       string `json:"msg"`
	SubMsg    string `json:"sub_msg"`
	CertifyID string `json:"certify_id"`
	Passed    string `json:"passed"`
}

func (n aliNode) errText() string {
	if strings.TrimSpace(n.SubMsg) != "" {
		return n.SubMsg
	}
	if strings.TrimSpace(n.Msg) != "" {
		return n.Msg
	}
	return "支付宝未返回错误原因（code " + n.Code + "）"
}

// Challenge 初始化认证，拿 certify_id 并组装扫码跳转地址。
func (a *Ali) Challenge(ctx context.Context, cfg Config, secret Secret, subject Subject) (Challenge, error) {
	if err := a.Validate(cfg, secret); err != nil {
		return Challenge{}, err
	}
	biz := map[string]any{
		"outer_order_no": "ZGYD20180913232" + strconv.FormatInt(a.now().Unix(), 10),
		"biz_code":       a.bizCode(cfg),
		"identity_param": map[string]string{
			"identity_type": "CERT_INFO",
			"cert_type":     "IDENTITY_CARD",
			"cert_name":     subject.RealName,
			"cert_no":       subject.IDNumber,
		},
	}
	if returnURL := cfg.Field("return_url"); returnURL != "" {
		biz["merchant_config"] = map[string]string{"return_url": returnURL}
	}
	bizContent, err := json.Marshal(biz)
	if err != nil {
		return Challenge{}, err
	}
	rawNode, err := a.call(ctx, cfg, secret, "alipay.user.certify.open.initialize", string(bizContent))
	if err != nil {
		return Challenge{}, err
	}
	var node aliNode
	if err := json.Unmarshal(rawNode, &node); err != nil {
		return Challenge{}, fmt.Errorf("支付宝初始化响应无法解析: %s", truncateCertLog(rawNode))
	}
	if node.Code != "10000" || node.CertifyID == "" {
		return Challenge{}, fmt.Errorf("支付宝实名认证初始化失败: %s", node.errText())
	}
	// 第二步在本地组装：pageExecute 只是把 certify_id 放进 biz_content 再签名。
	certifyBiz, err := json.Marshal(map[string]string{"certify_id": node.CertifyID})
	if err != nil {
		return Challenge{}, err
	}
	pageParams, err := a.signParams(cfg, secret, "alipay.user.certify.open.certify", string(certifyBiz))
	if err != nil {
		return Challenge{}, err
	}
	return Challenge{
		Provider: "ali",
		Token:    node.CertifyID,
		URL:      a.gateway(cfg) + "?" + pageParams.Encode(),
		Message:  "请使用支付宝扫描二维码完成实名认证",
	}, nil
}

// Query 轮询认证结果；passed 为空表示还在认证中。
func (a *Ali) Query(ctx context.Context, cfg Config, secret Secret, token string) (Result, error) {
	if err := a.Validate(cfg, secret); err != nil {
		return Result{}, err
	}
	bizContent, err := json.Marshal(map[string]string{"certify_id": token})
	if err != nil {
		return Result{}, err
	}
	rawNode, err := a.call(ctx, cfg, secret, "alipay.user.certify.open.query", string(bizContent))
	if err != nil {
		return Result{}, err
	}
	var node aliNode
	if err := json.Unmarshal(rawNode, &node); err != nil {
		return Result{}, fmt.Errorf("支付宝查询响应无法解析: %s", truncateCertLog(rawNode))
	}
	if node.Code != "10000" {
		return Result{}, fmt.Errorf("支付宝实名认证查询失败: %s", node.errText())
	}
	switch strings.ToUpper(strings.TrimSpace(node.Passed)) {
	case "T":
		return Result{Match: true, Message: "审核通过"}, nil
	case "F":
		return Result{Match: false, Message: firstNonEmptyCert(node.SubMsg, node.Msg, "支付宝审核未通过")}, nil
	default:
		return Result{Pending: true, Message: "已提交资料，等待支付宝审核"}, nil
	}
}

// Verify 扫码类通道不做同步核验：保留该方法是为了满足 Provider 接口，
// 真正的认证流程走 Challenge + Query。
func (a *Ali) Verify(context.Context, Config, Secret, Subject) (Result, error) {
	return Result{}, ErrChallengeRequired
}
