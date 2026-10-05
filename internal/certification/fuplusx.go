// 涪擎实名认证（高级版）（对应魔方 CBAP 插件 certification/fuplusx，
// 阿里云云市场 cmapi028323，默认端点 fephone.market.alicloudapi.com）。
//
// 一个通道按配置覆盖四种要素（与插件 FuplusxCollectionInfo 一致）：
//
//   - 2：姓名 + 身份证号（/IDCard）
//
//   - 3：姓名 + 身份证号 + 银行卡号（/bankCheck）
//
//   - 4：姓名 + 身份证号 + 手机号（/phoneCheck）
//
//   - 5：姓名 + 身份证号 + 银行卡号 + 手机号（/bankCheck4）
//
//     GET {base}{path}?idCard=<证件号>&name=<urlencode(姓名)>[&accountNo=][&mobile=]
//     Authorization: APPCODE <app_code>
//
// 判据：HTTP 200 且响应 status == "01"（traceId 作为凭证带出）；其余带出 msg。
// 与参考实现的一点差异：type=4 也把手机号声明为必填收集项——插件从认证记录
// 里取 phone，而本系统的提交只有用户填写的扩展字段，不收集必然发空值。
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

// Fuplusx 实现涪擎实名认证（高级版）通道。
type Fuplusx struct {
	// Endpoint 可覆盖（测试指向本地假服务）。
	Endpoint string
	http     *http.Client
}

// NewFuplusx 构造通道。
func NewFuplusx() *Fuplusx {
	return &Fuplusx{http: &http.Client{Timeout: 15 * time.Second}}
}

// Name 返回通道标识。
func (f *Fuplusx) Name() string { return "fuplusx" }

func init() { Register(NewFuplusx()) }

// Validate 检查云市场授权码与要素类型。
func (f *Fuplusx) Validate(cfg Config, secret Secret) error {
	if secret.Get("app_code") == "" {
		return fmt.Errorf("涪擎实名认证需要 app_code（云市场授权码）")
	}
	switch cfg.Field("type") {
	case "", "2", "3", "4", "5":
		return nil
	default:
		return fmt.Errorf("涪擎实名认证的 type 只能是 2 / 3 / 4 / 5")
	}
}

// elementType 返回要素类型，缺省为 2（二要素）。
func (f *Fuplusx) elementType(cfg Config) int {
	v, err := strconv.Atoi(cfg.Field("type"))
	if err != nil || v < 2 || v > 5 {
		return 2
	}
	return v
}

// Fields 声明额外输入：银行卡三要素收银行卡号、手机三要素收手机号、四要素都收。
func (f *Fuplusx) Fields(cfg Config) []Field {
	switch f.elementType(cfg) {
	case 3:
		return []Field{{Key: "bank", Label: "银行卡号", Placeholder: "请输入银行卡号", Required: true}}
	case 4:
		return []Field{{Key: "phone", Label: "手机号", Placeholder: "请输入手机号", Required: true}}
	case 5:
		return []Field{
			{Key: "bank", Label: "银行卡号", Placeholder: "请输入银行卡号", Required: true},
			{Key: "phone", Label: "手机号", Placeholder: "请输入手机号", Required: true},
		}
	}
	return nil
}

// path 返回要素类型对应的资源路径（与插件一致）。
func (f *Fuplusx) path(kind int) string {
	switch kind {
	case 3:
		return "/bankCheck"
	case 4:
		return "/phoneCheck"
	case 5:
		return "/bankCheck4"
	default:
		return "/IDCard"
	}
}

// Verify 调用涪擎实名认证接口。
func (f *Fuplusx) Verify(ctx context.Context, cfg Config, secret Secret, subject Subject) (Result, error) {
	if err := f.Validate(cfg, secret); err != nil {
		return Result{}, err
	}
	kind := f.elementType(cfg)
	base := f.Endpoint
	if base == "" {
		base = strings.TrimRight(cfg.Field("endpoint"), "/")
	}
	if base == "" {
		base = "https://fephone.market.alicloudapi.com"
	}
	q := url.Values{"idCard": {subject.IDNumber}, "name": {subject.RealName}}
	switch kind {
	case 3:
		q.Set("accountNo", subject.ExtraField("bank"))
	case 4:
		q.Set("mobile", subject.ExtraField("phone"))
	case 5:
		q.Set("accountNo", subject.ExtraField("bank"))
		q.Set("mobile", subject.ExtraField("phone"))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+f.path(kind)+"?"+q.Encode(), nil)
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "APPCODE "+secret.Get("app_code"))
	resp, err := f.http.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("涪擎实名认证请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != 200 {
		return Result{}, fmt.Errorf("涪擎实名认证失败: %s", alicaMarketHTTPError(resp.StatusCode, resp.Header.Get("X-Ca-Error-Message")))
	}
	var out struct {
		Status  string `json:"status"`
		Msg     string `json:"msg"`
		TraceID string `json:"traceId"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Result{}, fmt.Errorf("涪擎实名认证返回无法解析(HTTP %d): %s", resp.StatusCode, truncateCertLog(body))
	}
	res := Result{Message: firstNonEmptyCert(out.Msg, "核验完成")}
	if out.TraceID != "" {
		res.Message += "（traceId: " + out.TraceID + "）"
	}
	// status=="01" 是「一致」；其余（02 不一致、03 查无此人等）是查到了但不匹配。
	res.Match = out.Status == "01"
	return res, nil
}
