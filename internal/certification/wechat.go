// 微信实名认证（对应魔方 public/plugins/certification/wechat，腾讯云慧眼人脸核身）。
//
// 两步（TC3-HMAC-SHA256 签名，internal/tc3 与短信/验证码共用同一份实现）：
//  1. DetectAuth：提交姓名/身份证 + RuleId，返回 BizToken 与认证地址（二维码内容）
//  2. GetDetectInfoEnhanced：用 BizToken 查询，Text.ErrCode==0 为通过
//
// 与参考实现不同的一点：查询异常（尚未完成、记录不存在等 Error 节点，以及网络
// 失败）由 API 层按「处理中」保持 pending，不直接判失败——插件把任何异常都置
// 为未通过，用户还没扫完码就会被判失败。
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

// Wechat 实现微信实名认证通道。
type Wechat struct {
	// Endpoint 可覆盖（测试指向本地假服务）。
	Endpoint string
	// Now 可注入时间（签名断言用）；为空则用 time.Now。
	Now  func() time.Time
	http *http.Client
}

// NewWechat 构造通道。
func NewWechat() *Wechat {
	return &Wechat{http: &http.Client{Timeout: 15 * time.Second}}
}

// Name 返回通道标识。
func (w *Wechat) Name() string { return "wechat" }

func init() { Register(NewWechat()) }

// ruleID 取数字 RuleId。
func (w *Wechat) ruleID(cfg Config) (int64, error) {
	id, err := strconv.ParseInt(cfg.Field("rule_id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, fmt.Errorf("微信实名认证需要数字 RuleId（人脸核身规则编号）")
	}
	return id, nil
}

// Validate 检查配置与凭据。
func (w *Wechat) Validate(cfg Config, secret Secret) error {
	if _, err := w.ruleID(cfg); err != nil {
		return err
	}
	if secret.Get("secret_id") == "" || secret.Get("secret_key") == "" {
		return fmt.Errorf("微信实名认证需要 SecretId 与 SecretKey")
	}
	return nil
}

// host 返回接口域名。
func (w *Wechat) host(cfg Config) string {
	if w.Endpoint != "" {
		return w.Endpoint
	}
	if v := cfg.Field("endpoint"); v != "" {
		return v
	}
	return "faceid.tencentcloudapi.com"
}

func (w *Wechat) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// call 调用 faceid 的一个动作，返回 Response 节点（Error 节点在这里就转成错误）。
func (w *Wechat) call(ctx context.Context, cfg Config, secret Secret, action string, payload any) (json.RawMessage, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	host := w.host(cfg)
	ts := w.now()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+host+"/", strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Version", "2018-03-01")
	req.Header.Set("X-TC-Timestamp", strconv.FormatInt(ts.Unix(), 10))
	req.Header.Set("Authorization", tc3.Authorization(secret.Get("secret_id"), secret.Get("secret_key"), "faceid", host, ts.UTC().Format("2006-01-02"), ts.Unix(), string(body)))
	resp, err := w.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("微信实名认证请求失败: %w", err)
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
		return nil, fmt.Errorf("微信实名认证返回无法解析(HTTP %d): %s", resp.StatusCode, truncateCertLog(raw))
	}
	var errNode struct {
		Error struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	}
	if err := json.Unmarshal(top.Response, &errNode); err != nil {
		return nil, fmt.Errorf("微信实名认证返回无法解析: %s", truncateCertLog(top.Response))
	}
	if errNode.Error.Code != "" {
		return nil, fmt.Errorf("微信实名认证失败: %s（%s）", errNode.Error.Message, errNode.Error.Code)
	}
	return top.Response, nil
}

// Challenge 调 DetectAuth 拿 BizToken 与微信扫码地址。
func (w *Wechat) Challenge(ctx context.Context, cfg Config, secret Secret, subject Subject) (Challenge, error) {
	if err := w.Validate(cfg, secret); err != nil {
		return Challenge{}, err
	}
	ruleID, _ := w.ruleID(cfg)
	rawNode, err := w.call(ctx, cfg, secret, "DetectAuth", map[string]any{
		"RuleId":      ruleID,
		"IdCard":      subject.IDNumber,
		"Name":        subject.RealName,
		"RedirectUrl": cfg.Field("return_url"),
	})
	if err != nil {
		return Challenge{}, err
	}
	var node struct {
		BizToken string `json:"BizToken"`
		URL      string `json:"Url"`
	}
	if err := json.Unmarshal(rawNode, &node); err != nil {
		return Challenge{}, fmt.Errorf("微信实名认证返回无法解析: %s", truncateCertLog(rawNode))
	}
	if node.BizToken == "" || node.URL == "" {
		return Challenge{}, fmt.Errorf("微信实名认证初始化失败: 上游未返回 BizToken/Url")
	}
	return Challenge{
		Provider: "wechat",
		Token:    node.BizToken,
		// 参考实现会对 Url 做 htmlspecialchars_decode（URL 里可能带 &amp;）。
		URL:     decodeHTMLEntities(node.URL),
		Message: "请使用微信扫描二维码完成实名认证",
	}, nil
}

// Query 轮询认证结果：Text 节点出现才算出了结果。
func (w *Wechat) Query(ctx context.Context, cfg Config, secret Secret, token string) (Result, error) {
	if err := w.Validate(cfg, secret); err != nil {
		return Result{}, err
	}
	ruleID, _ := w.ruleID(cfg)
	rawNode, err := w.call(ctx, cfg, secret, "GetDetectInfoEnhanced", map[string]any{
		"BizToken": token,
		"RuleId":   ruleID,
		"InfoType": "1",
	})
	if err != nil {
		return Result{}, err
	}
	var node struct {
		Text *struct {
			ErrCode int64  `json:"ErrCode"`
			ErrMsg  string `json:"ErrMsg"`
		} `json:"Text"`
	}
	if err := json.Unmarshal(rawNode, &node); err != nil {
		return Result{}, fmt.Errorf("微信实名认证返回无法解析: %s", truncateCertLog(rawNode))
	}
	if node.Text == nil {
		return Result{Pending: true, Message: "等待用户完成微信人脸核身"}, nil
	}
	if node.Text.ErrCode == 0 {
		return Result{Match: true, Message: "认证通过"}, nil
	}
	return Result{Match: false, Message: firstNonEmptyCert(node.Text.ErrMsg, "微信实名认证未通过")}, nil
}

// decodeHTMLEntities 等价于 PHP htmlspecialchars_decode 的五个基本实体。
func decodeHTMLEntities(s string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", "\"", "&#039;", "'")
	return r.Replace(s)
}

// Verify 扫码类通道不做同步核验：保留该方法是为了满足 Provider 接口，
// 真正的认证流程走 Challenge + Query。
func (w *Wechat) Verify(context.Context, Config, Secret, Subject) (Result, error) {
	return Result{}, ErrChallengeRequired
}
