// 阿里云云市场身份证二要素核验（对应魔方 public/plugins/certification/alitwo）。
//
// 协议（参考插件 logic/Alitwo.php，阿里云云市场「简单身份认证」模式）：
//
//	GET {url}{path}?idCard=<证件号>&name=<urlencode(姓名)>
//	Authorization: APPCODE <app_code>
//	默认端点 https://idcert.market.alicloudapi.com/idcard
//
// 判据：HTTP 200 且响应 status == "01" → 一致；其余一律 Match=false，
// msg 带出原因（HTTP 错误码的分类提示与插件一致）。traceId 作为凭证返回。
package certification

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Alitwo 实现阿里云身份证二要素核验通道。
type Alitwo struct {
	// Endpoint 可覆盖，便于测试指向本地假服务。
	Endpoint string
	http     *http.Client
}

// NewAlitwo 构造通道。
func NewAlitwo() *Alitwo {
	return &Alitwo{http: &http.Client{Timeout: 15 * time.Second}}
}

// Name 返回通道标识（沿用魔方插件标识）。
func (a *Alitwo) Name() string { return "alitwo" }

func init() { Register(NewAlitwo()) }

// Validate 检查必填配置。
func (a *Alitwo) Validate(cfg Config, secret Secret) error {
	if secret.Get("app_code") == "" {
		return fmt.Errorf("阿里云二要素核验需要 app_code（云市场授权码）")
	}
	return nil
}

// Verify 调用二要素核验接口。
func (a *Alitwo) Verify(ctx context.Context, cfg Config, secret Secret, subject Subject) (Result, error) {
	if err := a.Validate(cfg, secret); err != nil {
		return Result{}, err
	}
	// 结构体字段是测试注入点；配置里的 endpoint 允许换云市场同协议的其他商品。
	endpoint := strings.TrimRight(cfg.Field("endpoint"), "/")
	if a.Endpoint != "" {
		endpoint = a.Endpoint
	}
	if endpoint == "" {
		endpoint = "https://idcert.market.alicloudapi.com/idcard"
	}
	target := endpoint + "?" + url.Values{
		"idCard": {subject.IDNumber},
		"name":   {subject.RealName},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "APPCODE "+secret.Get("app_code"))
	resp, err := a.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("二要素核验请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, err
	}
	var out struct {
		Status  string `json:"status"`
		Msg     string `json:"msg"`
		TraceID string `json:"traceId"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, fmt.Errorf("二要素核验返回无法解析(HTTP %d): %s", resp.StatusCode, truncateCertLog(body))
	}
	// HTTP 层错误按云市场网关的分类给出人话（与插件逻辑一致）。
	if resp.StatusCode != 200 {
		return Result{}, fmt.Errorf("二要素核验失败: %s", alicaMarketHTTPError(resp.StatusCode, resp.Header.Get("X-Ca-Error-Message")))
	}
	res := Result{Message: firstNonEmptyCert(out.Msg, "核验完成")}
	if out.TraceID != "" {
		res.Message += "（traceId: " + out.TraceID + "）"
	}
	// status=="01" 是「一致」；其余（02 不一致等）是查到了但不匹配。
	res.Match = out.Status == "01"
	return res, nil
}

// alicaMarketHTTPError 把云市场网关的错误码翻译成与插件一致的提示。
func alicaMarketHTTPError(code int, caMsg string) string {
	switch code {
	case 400:
		return "参数错误（AppCode 或请求参数不合法）"
	case 403:
		return "服务未授权或套餐包次数用完"
	case 500:
		return "API 网关错误"
	default:
		if strings.TrimSpace(caMsg) != "" {
			return caMsg
		}
		return fmt.Sprintf("HTTP %d", code)
	}
}

func truncateCertLog(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}

func firstNonEmptyCert(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
