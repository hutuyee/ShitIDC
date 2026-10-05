// 智简魔方芝麻信用（对应魔方 public/plugins/certification/idcsmartali）。
//
// 协议：POST http://api1.idcsmart.com/certapi.php?action=initialize|certify|query
//
//	请求头：api / key（魔方后台签发的商户参数）
//	表单参数：outer_order_no、biz_code、cert_type、cert_name、cert_no、return_url
//	返回：JSON（status 200 表示成功，其余带 msg 原因）
//
// 判据：initialize / certify 必须 status==200；query 的 status==200 表示认证通过，
// 其余一律「处理中」等下一次轮询——参考实现把非 200 直接置为未通过，用户还没扫码
// 就会被判失败；这里与插件文档「默认状态 4 = 已提交资料」保持一致。
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
)

// Idcsmartali 实现智简魔方芝麻信用通道。
type Idcsmartali struct {
	// Endpoint 可覆盖（测试指向本地假服务）。
	Endpoint string
	http     *http.Client
}

// NewIdcsmartali 构造通道。
func NewIdcsmartali() *Idcsmartali {
	return &Idcsmartali{http: &http.Client{Timeout: 20 * time.Second}}
}

// Name 返回通道标识。
func (i *Idcsmartali) Name() string { return "idcsmartali" }

func init() { Register(NewIdcsmartali()) }

// Validate 检查商户凭据。
func (i *Idcsmartali) Validate(_ Config, secret Secret) error {
	if secret.Get("api") == "" || secret.Get("key") == "" {
		return fmt.Errorf("智简魔方芝麻信用需要 api 与 key（魔方后台签发）")
	}
	return nil
}

// endpoint 返回接口地址。
func (i *Idcsmartali) endpoint(cfg Config) string {
	if i.Endpoint != "" {
		return i.Endpoint
	}
	if v := cfg.Field("endpoint"); v != "" {
		return v
	}
	return "http://api1.idcsmart.com/certapi.php"
}

// call 发一次 API 请求，返回解析后的 JSON。
func (i *Idcsmartali) call(ctx context.Context, cfg Config, secret Secret, action string, form url.Values) (map[string]any, error) {
	if err := i.Validate(cfg, secret); err != nil {
		return nil, err
	}
	target := i.endpoint(cfg) + "?action=" + url.QueryEscape(action)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("api", secret.Get("api"))
	req.Header.Set("key", secret.Get("key"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := i.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("实名认证请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	// 参考实现会去掉 UTF-8 BOM；json 解析器不容忍 BOM，这里照做。
	body = []byte(strings.TrimPrefix(string(body), "\ufeff"))
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("实名认证返回无法解析(HTTP %d): %s", resp.StatusCode, truncateCertLog(body))
	}
	return out, nil
}

// certStatusCode 读 status 字段：数字或字符串都认（PHP 松散比较的等价物）。
func certStatusCode(v any) int {
	switch t := v.(type) {
	case float64:
		return int(t)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err == nil {
			return n
		}
	}
	return 0
}

// certString 把 JSON 里的值转成字符串（msg 可能是数字/字符串）。
func certString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	default:
		return ""
	}
}

// Challenge 初始化并生成扫码地址。
func (i *Idcsmartali) Challenge(ctx context.Context, cfg Config, secret Secret, subject Subject) (Challenge, error) {
	form := url.Values{}
	form.Set("outer_order_no", "ZGYD20180913232"+strconv.FormatInt(time.Now().Unix(), 10))
	form.Set("biz_code", firstNonEmptyCert(cfg.Field("biz_code"), "FACE"))
	form.Set("cert_type", firstNonEmptyCert(cfg.Field("cert_type"), "IDENTITY_CARD"))
	form.Set("cert_name", subject.RealName)
	form.Set("cert_no", subject.IDNumber)
	if v := cfg.Field("return_url"); v != "" {
		form.Set("return_url", v)
	}
	init, err := i.call(ctx, cfg, secret, "initialize", form)
	if err != nil {
		return Challenge{}, err
	}
	if certStatusCode(init["status"]) != 200 {
		return Challenge{}, fmt.Errorf("实名认证初始化失败: %s", firstNonEmptyCert(certString(init["msg"]), "上游未返回原因"))
	}
	token := certString(init["certify_id"])
	if token == "" {
		return Challenge{}, fmt.Errorf("实名认证初始化失败: 上游未返回 certify_id")
	}
	certify, err := i.call(ctx, cfg, secret, "certify", url.Values{"certify_id": {token}})
	if err != nil {
		return Challenge{}, err
	}
	link := certString(certify["url"])
	if link == "" {
		return Challenge{}, fmt.Errorf("实名认证初始化失败: 上游未返回认证地址（%s）", firstNonEmptyCert(certString(certify["msg"]), "无原因"))
	}
	return Challenge{
		Provider: "idcsmartali",
		Token:    token,
		URL:      link,
		Message:  "请使用支付宝扫描二维码完成实名认证",
	}, nil
}

// Query 查询认证进度；非 200 一律按处理中返回。
func (i *Idcsmartali) Query(ctx context.Context, cfg Config, secret Secret, token string) (Result, error) {
	out, err := i.call(ctx, cfg, secret, "query", url.Values{"certify_id": {token}})
	if err != nil {
		return Result{}, err
	}
	if certStatusCode(out["status"]) == 200 {
		return Result{Match: true, Message: firstNonEmptyCert(certString(out["msg"]), "审核通过")}, nil
	}
	return Result{Pending: true, Message: firstNonEmptyCert(certString(out["msg"]), "已提交资料，等待审核")}, nil
}

// Verify 扫码类通道不做同步核验：保留该方法是为了满足 Provider 接口，
// 真正的认证流程走 Challenge + Query。
func (i *Idcsmartali) Verify(context.Context, Config, Secret, Subject) (Result, error) {
	return Result{}, ErrChallengeRequired
}
