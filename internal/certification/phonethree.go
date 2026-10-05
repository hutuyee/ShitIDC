// 手机三要素核验（对应魔方 public/plugins/certification/phonethree，
// 阿里云云市场 cmapi031847）。
//
//	GET {url}?idcard=&phone=&realname=<urlencode(姓名)>
//	Authorization: APPCODE <app_code>
//
// 判据：code==200 即一致；订单号 ordersign 作为凭证。
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

// Phonethree 实现手机三要素核验通道。
type Phonethree struct {
	// Endpoint 可覆盖（测试指向本地假服务）。
	Endpoint string
	http     *http.Client
}

// NewPhonethree 构造通道。
func NewPhonethree() *Phonethree {
	return &Phonethree{http: &http.Client{Timeout: 15 * time.Second}}
}

// Name 返回通道标识。
func (p *Phonethree) Name() string { return "phonethree" }

func init() { Register(NewPhonethree()) }

// Validate 检查云市场授权码。
func (p *Phonethree) Validate(_ Config, secret Secret) error {
	if secret.Get("app_code") == "" {
		return fmt.Errorf("手机三要素核验需要 app_code（云市场授权码）")
	}
	return nil
}

// Fields 声明额外输入：手机号（与证件号一致）。
func (p *Phonethree) Fields(_ Config) []Field {
	return []Field{{Key: "phone", Label: "手机号", Placeholder: "请输入手机号", Required: true}}
}

// endpoint 返回核验地址。
func (p *Phonethree) endpoint(cfg Config) string {
	if p.Endpoint != "" {
		return p.Endpoint
	}
	if v := cfg.Field("endpoint"); v != "" {
		return v
	}
	return "https://phone3.market.alicloudapi.com/phonethree"
}

// Verify 调用手机三要素核验。
func (p *Phonethree) Verify(ctx context.Context, cfg Config, secret Secret, subject Subject) (Result, error) {
	if err := p.Validate(cfg, secret); err != nil {
		return Result{}, err
	}
	q := url.Values{}
	q.Set("idcard", subject.IDNumber)
	q.Set("phone", subject.ExtraField("phone"))
	q.Set("realname", subject.RealName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.endpoint(cfg)+"?"+q.Encode(), nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "APPCODE "+secret.Get("app_code"))
	resp, err := p.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("手机三要素核验请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, err
	}
	var out struct {
		Code      any    `json:"code"`
		Msg       string `json:"msg"`
		OrderSign string `json:"ordersign"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, fmt.Errorf("手机三要素核验返回无法解析(HTTP %d): %s", resp.StatusCode, truncateCertLog(body))
	}
	if resp.StatusCode != 200 {
		return Result{}, fmt.Errorf("手机三要素核验失败: %s", alicaMarketHTTPError(resp.StatusCode, resp.Header.Get("X-Ca-Error-Message")))
	}
	res := Result{Message: firstNonEmptyCert(out.Msg, "核验完成")}
	if strings.TrimSpace(out.OrderSign) != "" {
		res.Message += "（ordersign: " + strings.TrimSpace(out.OrderSign) + "）"
	}
	res.Match = certStatusCode(out.Code) == 200
	return res, nil
}
