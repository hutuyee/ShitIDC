// E证通人脸认证（对应魔方 CBAP 插件 certification/yerzt，腾讯云 faceid
// GetEidToken / CheckEidTokenStatus / GetEidResult，TC3-HMAC-SHA256 签名
// 复用 internal/tc3，与微信人脸核身同源）。
//
// 扫码流程：GetEidToken 提交姓名/身份证与 MerchantId，返回 EidToken 与认证
// 地址（二维码内容）；轮询先 CheckEidTokenStatus：
//   - Status=="timeout" → 本次会话已过期，保持处理中并提示重新发起
//   - 其余 → GetEidResult(InfoType=0)，Text 节点出现才算出了结果：
//     ErrCode==0 通过，非 0 判未通过
//
// 与插件的一点差异：插件把 Text.ErrCode!=0 也当「尚未通过」继续轮询，前端永远
// 不会收敛；本实现按已出的结果判失败，用户可以直接重新提交。
// 另一个差异：input_type 缺省用 3（用户手动输入）——本系统在提交前已经收集了
// 姓名与证件号，扫码端不必再做 OCR。
package certification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/tc3"
)

// Yerzt 实现 E证通人脸认证通道。
type Yerzt struct {
	// Endpoint 可覆盖（测试指向本地假服务）。
	Endpoint string
	// Now 可注入时间（签名断言用）；为空则用 time.Now。
	Now  func() time.Time
	http *http.Client
}

// NewYerzt 构造通道。
func NewYerzt() *Yerzt {
	return &Yerzt{http: &http.Client{Timeout: 15 * time.Second}}
}

// Name 返回通道标识。
func (y *Yerzt) Name() string { return "yerzt" }

func init() { Register(NewYerzt()) }

// Validate 检查商户号、腾讯云凭据与认证方式。
func (y *Yerzt) Validate(cfg Config, secret Secret) error {
	if cfg.Field("merchant_id") == "" {
		return fmt.Errorf("E证通需要 MerchantId（E证通商户号）")
	}
	switch cfg.Field("input_type") {
	case "", "1", "2", "3", "4":
	default:
		return fmt.Errorf("E证通的 input_type 只能是 1 / 2 / 3 / 4")
	}
	if secret.Get("secret_id") == "" || secret.Get("secret_key") == "" {
		return fmt.Errorf("E证通需要 SecretId 与 SecretKey")
	}
	return nil
}

// host 返回接口域名。
func (y *Yerzt) host(cfg Config) string {
	if y.Endpoint != "" {
		return y.Endpoint
	}
	if v := cfg.Field("endpoint"); v != "" {
		return v
	}
	return "faceid.tencentcloudapi.com"
}

func (y *Yerzt) now() time.Time {
	if y.Now != nil {
		return y.Now()
	}
	return time.Now()
}

// call 调用 faceid 的一个动作，返回 Response 节点（Error 节点在这里就转成错误，
// 轮询路径上由 API 层按「处理中」兜底）。
func (y *Yerzt) call(ctx context.Context, cfg Config, secret Secret, action string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	host := y.host(cfg)
	ts := y.now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", "2018-03-01")
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(ts.Unix(), 10))
	req.Header.Set("Authorization", tc3.Authorization(secret.Get("secret_id"), secret.Get("secret_key"), "faceid", host, ts.UTC().Format("2006-01-02"), ts.Unix(), string(body)))
	resp, err := y.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("E证通请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	var top struct {
		Response json.RawMessage `json:"Response"`
	}
	if err := json.Unmarshal(raw, &top); err != nil || len(top.Response) == 0 {
		return nil, fmt.Errorf("E证通返回无法解析(HTTP %d): %s", resp.StatusCode, truncateCertLog(raw))
	}
	var errNode struct {
		Error struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := json.Unmarshal(top.Response, &errNode); err != nil {
		return nil, fmt.Errorf("E证通返回无法解析: %s", truncateCertLog(top.Response))
	}
	if errNode.Error.Code != "" {
		return nil, fmt.Errorf("E证通失败: %s（%s）", errNode.Error.Message, errNode.Error.Code)
	}
	return top.Response, nil
}

// Challenge 调 GetEidToken 拿 EidToken 与扫码地址。
func (y *Yerzt) Challenge(ctx context.Context, cfg Config, secret Secret, subject Subject) (Challenge, error) {
	if err := y.Validate(cfg, secret); err != nil {
		return Challenge{}, err
	}
	inputType := cfg.Field("input_type")
	if inputType == "" {
		inputType = "3"
	}
	payload := map[string]any{
		"IdCard":     subject.IDNumber,
		"Name":       subject.RealName,
		"MerchantId": cfg.Field("merchant_id"),
		"Config":     map[string]any{"InputType": inputType},
	}
	if v := cfg.Field("return_url"); v != "" {
		payload["RedirectUrl"] = v
	}
	rawNode, err := y.call(ctx, cfg, secret, "GetEidToken", payload)
	if err != nil {
		return Challenge{}, err
	}
	var node struct {
		EidToken string `json:"EidToken"`
		URL      string `json:"Url"`
	}
	if err := json.Unmarshal(rawNode, &node); err != nil {
		return Challenge{}, fmt.Errorf("E证通返回无法解析: %s", truncateCertLog(rawNode))
	}
	if node.EidToken == "" || node.URL == "" {
		return Challenge{}, fmt.Errorf("E证通初始化失败: 上游未返回 EidToken/Url")
	}
	return Challenge{
		Provider: "yerzt",
		Token:    node.EidToken,
		// 参考实现会对 Url 做 htmlspecialchars_decode（URL 里可能带 &amp;）。
		URL:     decodeHTMLEntities(node.URL),
		Message: "请使用微信扫一扫完成 E证通人脸认证",
	}, nil
}

// Query 轮询认证结果：超时会话保持处理中，Text 节点出现才算出了结果。
func (y *Yerzt) Query(ctx context.Context, cfg Config, secret Secret, token string) (Result, error) {
	if err := y.Validate(cfg, secret); err != nil {
		return Result{}, err
	}
	rawStatus, err := y.call(ctx, cfg, secret, "CheckEidTokenStatus", map[string]any{"EidToken": token})
	if err != nil {
		return Result{}, err
	}
	var status struct {
		Status string `json:"Status"`
	}
	if err := json.Unmarshal(rawStatus, &status); err != nil {
		return Result{}, fmt.Errorf("E证通返回无法解析: %s", truncateCertLog(rawStatus))
	}
	if status.Status == "timeout" {
		return Result{Pending: true, Message: "本次验证已超时，请重新发起实名认证"}, nil
	}
	rawResult, err := y.call(ctx, cfg, secret, "GetEidResult", map[string]any{"EidToken": token, "InfoType": "0"})
	if err != nil {
		return Result{}, err
	}
	var node struct {
		Text *struct {
			ErrCode int64  `json:"ErrCode"`
			ErrMsg  string `json:"ErrMsg"`
			LiveMsg string `json:"LiveMsg"`
		} `json:"Text"`
	}
	if err := json.Unmarshal(rawResult, &node); err != nil {
		return Result{}, fmt.Errorf("E证通返回无法解析: %s", truncateCertLog(rawResult))
	}
	if node.Text == nil {
		return Result{Pending: true, Message: "等待用户完成 E证通人脸认证"}, nil
	}
	if node.Text.ErrCode == 0 {
		msg := "认证通过"
		if node.Text.LiveMsg != "" {
			msg = "认证" + node.Text.LiveMsg
		}
		return Result{Match: true, Message: msg}, nil
	}
	return Result{Match: false, Message: firstNonEmptyCert(node.Text.ErrMsg, "E证通实名认证未通过")}, nil
}

// Verify 扫码类通道不做同步核验：保留该方法是为了满足 Provider 接口。
func (y *Yerzt) Verify(context.Context, Config, Secret, Subject) (Result, error) {
	return Result{}, ErrChallengeRequired
}
