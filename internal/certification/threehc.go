// 银行卡三/四要素核验（对应魔方 public/plugins/certification/threehc，
// 「三要素--深圳华辰」，阿里云云市场 cmapi025566）。
//
//	GET {base}/cert/bank-card/{type}?bank=&name=&number=&type=0[&mobile=]
//	Authorization: APPCODE <app_code>
//
// 参数与参考实现一致：number 传身份证号、bank 传银行卡号；要素类型由配置决定：
//   - 2：姓名 + 银行卡号
//   - 3：姓名 + 身份证号 + 银行卡号
//   - 4：三要素再加银行预留手机号
//
// 判据：ret==200 且 data.desc=="一致"；其余原样带出 desc/msg。
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

// Threehc 实现银行卡要素核验通道。
type Threehc struct {
	// Endpoint 可覆盖（测试指向本地假服务）。
	Endpoint string
	http     *http.Client
}

// NewThreehc 构造通道。
func NewThreehc() *Threehc {
	return &Threehc{http: &http.Client{Timeout: 15 * time.Second}}
}

// Name 返回通道标识。
func (t *Threehc) Name() string { return "threehc" }

func init() { Register(NewThreehc()) }

// Validate 检查云市场授权码与要素类型。
func (t *Threehc) Validate(cfg Config, secret Secret) error {
	if secret.Get("app_code") == "" {
		return fmt.Errorf("三要素核验需要 app_code（云市场授权码）")
	}
	switch cfg.Field("type") {
	case "", "2", "3", "4":
		return nil
	default:
		return fmt.Errorf("三要素核验的 type 只能是 2 / 3 / 4")
	}
}

// elementType 返回要素类型，缺省为 3（三要素）。
func (t *Threehc) elementType(cfg Config) int {
	v, err := strconv.Atoi(cfg.Field("type"))
	if err != nil || (v != 2 && v != 3 && v != 4) {
		return 3
	}
	return v
}

// Fields 声明额外输入：2/3 要素只收银行卡号，四要素再收手机号。
func (t *Threehc) Fields(cfg Config) []Field {
	out := []Field{{Key: "bank", Label: "银行卡号", Placeholder: "请输入银行卡号", Required: true}}
	if t.elementType(cfg) == 4 {
		out = append(out, Field{Key: "phone", Label: "银行预留手机号", Placeholder: "请输入银行预留手机号", Required: true})
	}
	return out
}

// Verify 调用银行卡要素核验。
func (t *Threehc) Verify(ctx context.Context, cfg Config, secret Secret, subject Subject) (Result, error) {
	if err := t.Validate(cfg, secret); err != nil {
		return Result{}, err
	}
	kind := t.elementType(cfg)
	q := url.Values{}
	q.Set("bank", subject.ExtraField("bank"))
	q.Set("name", subject.RealName)
	switch kind {
	case 2:
		// type=2 只传姓名 + 银行卡（参考实现如此）。
	case 3:
		q.Set("number", subject.IDNumber)
		q.Set("type", "0")
	default:
		q.Set("number", subject.IDNumber)
		q.Set("type", "0")
		q.Set("mobile", subject.ExtraField("phone"))
	}
	base := t.Endpoint
	if base == "" {
		base = strings.TrimRight(cfg.Field("endpoint"), "/")
	}
	if base == "" {
		base = "https://api11.aliyun.venuscn.com"
	}
	target := base + "/cert/bank-card/" + strconv.Itoa(kind) + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "APPCODE "+secret.Get("app_code"))
	resp, err := t.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("三要素核验请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, err
	}
	var out struct {
		Ret   any    `json:"ret"`
		Msg   string `json:"msg"`
		LogID string `json:"log_id"`
		Data  struct {
			Desc string `json:"desc"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, fmt.Errorf("三要素核验返回无法解析(HTTP %d): %s", resp.StatusCode, truncateCertLog(body))
	}
	if resp.StatusCode != 200 {
		return Result{}, fmt.Errorf("三要素核验失败: %s", alicaMarketHTTPError(resp.StatusCode, resp.Header.Get("X-Ca-Error-Message")))
	}
	res := Result{Message: firstNonEmptyCert(out.Data.Desc, out.Msg, "核验完成")}
	if out.LogID != "" {
		res.Message += "（log_id: " + out.LogID + "）"
	}
	res.Match = certStatusCode(out.Ret) == 200 && out.Data.Desc == "一致"
	return res, nil
}
