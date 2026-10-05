// Package custom 是声明式 HTTP 上游运行时：执行 zjmfimport 生成的
// ProviderSpec，把魔方 server 插件的生命周期调用翻译成对上游的 HTTP 请求。
//
// 设计边界：
//   - 规格里没有的动作返回明确错误，绝不猜测路径；
//   - 签名只实现转换器识别出的两种已知方案（md5sort_upper / md5concat），
//     token 不落日志；
//   - 成功判据按规格的 success_when 做数字宽容比较（"1" 等于 1、1.0），
//     失败文案取上游响应的 message 字段透出给运营；
//   - 所有请求都经 SSRF 防护的 HTTP 客户端出网。
package custom

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/model"
	"github.com/hutuyee/ShitIDC/internal/provider"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/zjmfimport"
)

// Options 是运行一个规格所需的连接参数。
type Options struct {
	BaseURL      string        // 接口地址，如 https://1.2.3.4:8888
	Token        string        // 接口密钥（对应魔方 accesshash / 服务器密码）
	Timeout      time.Duration // 默认 30s
	AllowPrivate bool          // 内网地址放行（魔方上游多为内网面板）
	// 测试注入：为空用默认实现。
	Client *http.Client
	Now    func() time.Time
	Random func() string
}

// Provider 实现 provider.Provider。
type Provider struct {
	spec zjmfimport.Spec
	opt  Options
	hc   *http.Client
}

// New 校验并构建一个声明式上游。
func New(spec zjmfimport.Spec, opt Options) (*Provider, error) {
	// 规格可能来自后台编辑器或旧版本转换器，先补默认值再校验，
	// 避免 InstanceIDParam 之类漏配导致开通号被静默丢弃。
	spec.Normalize()
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opt.BaseURL) == "" {
		return nil, errors.New("缺少接口地址（BaseURL）")
	}
	if strings.TrimSpace(opt.Token) == "" {
		return nil, errors.New("缺少接口密钥（Token）")
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 30 * time.Second
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Random == nil {
		opt.Random = func() string { return strconv.FormatInt(cryptorandInt(), 10) }
	}
	if opt.Client == nil {
		opt.Client = security.SafeHTTPClient(opt.AllowPrivate, opt.Timeout)
	}
	return &Provider{spec: spec, opt: opt, hc: opt.Client}, nil
}

// FromProvider 从 providers 表的行构建：规格存 config["spec"]，token 是解密后的密钥。
func FromProvider(pv model.Provider, secret string) (*Provider, error) {
	raw, err := json.Marshal(pv.Config["spec"])
	if err != nil {
		return nil, fmt.Errorf("custom 规格读取失败: %w", err)
	}
	var spec zjmfimport.Spec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, fmt.Errorf("custom 规格不是合法 JSON: %w", err)
	}
	return New(spec, Options{BaseURL: pv.BaseURL, Token: secret, AllowPrivate: configAllowPrivate(pv.Config)})
}

func configAllowPrivate(cfg map[string]any) bool {
	if v, ok := cfg["allow_private"].(bool); ok {
		return v
	}
	return false
}

func cryptorandInt() int64 {
	var b [8]byte
	_, _ = rand.Read(b[:])
	var n int64
	for _, c := range b {
		n = n<<8 | int64(c)
	}
	if n < 0 {
		n = -n
	}
	return n
}

// ---- provider.Provider ----

func (p *Provider) TestConnection(ctx context.Context) error {
	act, ok := p.spec.Actions[zjmfimport.ActionTest]
	if !ok || act.Path == "" {
		return errors.New("规格未配置 test 动作（连接测试需要上游路径）")
	}
	_, err := p.call(ctx, act, map[string]string{}, nil)
	return err
}

func (p *Provider) Create(ctx context.Context, req provider.CreateRequest) (*provider.Instance, error) {
	act, ok := p.spec.Actions[zjmfimport.ActionCreate]
	if !ok || act.Path == "" {
		return nil, errors.New("规格未配置 create 动作（该插件无法自动开通）")
	}
	vars := varsFromCreate(req)
	resp, err := p.call(ctx, act, vars, nil)
	if err != nil {
		return nil, err
	}
	if act.InstanceIDPath == "" {
		return nil, errors.New("规格未配置 instance_id_path，无法确定开通号")
	}
	id := jsonStringAt(resp, act.InstanceIDPath)
	if id == "" {
		return nil, fmt.Errorf("开通响应里没有取到开通号（instance_id_path=%s）", act.InstanceIDPath)
	}
	return &provider.Instance{ID: id, Data: resp}, nil
}

func (p *Provider) Suspend(ctx context.Context, id string) error {
	return p.simpleAction(ctx, zjmfimport.ActionSuspend, "暂停", id)
}

func (p *Provider) Unsuspend(ctx context.Context, id string) error {
	return p.simpleAction(ctx, zjmfimport.ActionUnsuspend, "解除暂停", id)
}

func (p *Provider) Terminate(ctx context.Context, id string) error {
	return p.simpleAction(ctx, zjmfimport.ActionTerminate, "删除", id)
}

func (p *Provider) Renew(ctx context.Context, req provider.RenewRequest) error {
	act, ok := p.spec.Actions[zjmfimport.ActionRenew]
	if !ok || act.Path == "" {
		return errors.New("规格未配置 renew 动作")
	}
	vars := map[string]string{"instance_id": req.InstanceID}
	if !req.ExpiresAt.IsZero() {
		vars["expire_date"] = req.ExpiresAt.Format("2006-01-02")
		vars["expire_ts"] = strconv.FormatInt(req.ExpiresAt.Unix(), 10)
	}
	_, err := p.call(ctx, act, vars, nil)
	return err
}

func (p *Provider) ChangePackage(ctx context.Context, req provider.ChangePackageRequest) error {
	act, ok := p.spec.Actions[zjmfimport.ActionChangePackage]
	if !ok || act.Path == "" {
		return provider.ErrChangePackageUnsupported
	}
	vars := map[string]string{"instance_id": req.InstanceID}
	for k, v := range flattenSelections(req.SelectionsJSON) {
		vars["opt_"+k] = v
	}
	_, err := p.call(ctx, act, vars, nil)
	return err
}

func (p *Provider) simpleAction(ctx context.Context, name, label, id string) error {
	act, ok := p.spec.Actions[name]
	if !ok || act.Path == "" {
		return fmt.Errorf("规格未配置 %s 动作", name)
	}
	if _, err := p.call(ctx, act, map[string]string{"instance_id": id}, nil); err != nil {
		return fmt.Errorf("%s失败: %w", label, err)
	}
	return nil
}

// ---- 请求执行 ----

// call 发起一次带签名的上游请求并按规格判定成功。
func (p *Provider) call(ctx context.Context, act zjmfimport.Action, vars map[string]string, extra map[string]string) (map[string]any, error) {
	if p.spec.Auth.Scheme == zjmfimport.SchemeUnsupported {
		return nil, errors.New("该规格使用无法静态转换的认证方式（如 Proxmox VE ticket），请使用 ShitIDC 内置供应商")
	}

	// 1. 组装业务参数与签名参数
	query := map[string]string{}
	for k, v := range act.Query {
		query[k] = expand(v, vars)
	}
	body := map[string]string{}
	for k, v := range act.Body {
		body[k] = expand(v, vars)
	}
	if id := vars["instance_id"]; id != "" && act.InstanceIDParam != "" {
		// 生命周期动作都带上游开通号；create 没有实例号。
		if _, exists := body[act.InstanceIDParam]; !exists {
			if _, exists = query[act.InstanceIDParam]; !exists {
				body[act.InstanceIDParam] = id
			}
		}
	}

	switch p.spec.Auth.Scheme {
	case zjmfimport.SchemeMD5SortUpper:
		now := fmt.Sprint(p.opt.Now().Unix())
		random := p.opt.Random()
		sig := md5SortUpper(now, random, p.opt.Token)
		if p.spec.Auth.Placement == zjmfimport.PlacementQuery {
			query[p.spec.Auth.TimeParam] = now
			query[p.spec.Auth.RandomParam] = random
			query[p.spec.Auth.SignatureParam] = sig
		} else {
			body[p.spec.Auth.TimeParam] = now
			body[p.spec.Auth.RandomParam] = random
			body[p.spec.Auth.SignatureParam] = sig
		}
	case zjmfimport.SchemeMD5Concat:
		// wlkanglepro 约定：s = md5(动作名 + token + 随机数)，
		// query 里 a=动作名、r=随机数、s=签名。
		random := p.opt.Random()
		sig := md5Upper(act.Path + p.opt.Token + random)
		query[p.spec.Auth.ActionParam] = act.Path
		query[p.spec.Auth.RandomParam] = random
		query[p.spec.Auth.SigParam] = sig
		for k, v := range p.spec.Auth.ExtraQuery {
			query[k] = v
		}
	}
	for k, v := range extra {
		body[k] = v
	}

	// 2. 发请求
	url := strings.TrimSuffix(p.opt.BaseURL, "/")
	// md5sort_upper 家族的动作路径拼在 URL 上（md5concat 的动作名走 query）。
	if p.spec.Auth.Scheme == zjmfimport.SchemeMD5SortUpper && act.Path != "" {
		if sub := strings.TrimPrefix(act.Path, "/"); sub != "" {
			if !strings.HasSuffix(url, "/") {
				url += "/"
			}
			url += sub
		}
	}
	method := strings.ToUpper(act.Method)
	if method != "GET" && method != "POST" {
		method = "POST"
	}
	var req *http.Request
	var err error
	if method == "GET" {
		// wlkanglepro 家族没有独立 path，动作在 query（a=）里；body 里
		// 的业务参数也一并进 query（file_get_contents 语义）。
		all := mergeMaps(body, query)
		if qs := encodeParams(all); qs != "" {
			url += "?" + qs
		}
		req, err = http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	} else {
		form := encodeParams(mergeMaps(body, query))
		req, err = http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(form))
	}
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ShitIDC-CustomProvider)")

	resp, err := p.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("上游请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("上游返回 HTTP %d", resp.StatusCode)
	}
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("上游返回不是 JSON: %w", err)
	}

	// 3. 判定成功
	if !successMatches(payload, p.spec.Success) {
		return payload, fmt.Errorf("上游返回失败: %s", jsonMessage(payload, p.spec.Success.MessageField))
	}
	return payload, nil
}

// md5SortUpper 实现魔方 md5sort_upper：[time, random, token] 字典序排序后
// 无分隔符拼接，md5 后转大写。与 bthosts_CreateSign（bthosts.php:14-23）
// 逐字节一致；token 不随请求发送。
func md5SortUpper(timeStr, random, token string) string {
	parts := []string{timeStr, random, token}
	sort.Strings(parts)
	return md5Upper(strings.Join(parts, ""))
}

func md5Upper(s string) string {
	sum := md5.Sum([]byte(s))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// successMatches 对 success_when 做数字宽容比较（"1" 等于 1、1.0）。
func successMatches(payload map[string]any, s zjmfimport.Success) bool {
	raw, ok := lookupPath(payload, s.Field)
	if !ok {
		return false
	}
	switch v := raw.(type) {
	case string:
		return strings.EqualFold(strings.TrimSpace(v), s.Equals)
	case float64:
		want, err := strconv.ParseFloat(s.Equals, 64)
		return err == nil && v == want
	case bool:
		want := s.Equals == "1" || strings.EqualFold(s.Equals, "true")
		return v == want
	default:
		b, _ := json.Marshal(v)
		return string(b) == s.Equals
	}
}

func jsonMessage(payload map[string]any, field string) string {
	if v := jsonStringAt(payload, field); v != "" {
		return v
	}
	b, _ := json.Marshal(payload)
	if len(b) > 300 {
		b = b[:300]
	}
	return string(b)
}

// lookupPath 按点分路径取嵌套 JSON 值。
func lookupPath(payload map[string]any, path string) (any, bool) {
	cur := any(payload)
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// jsonStringAt 取路径处的字符串形式值（数字自动转字符串）。
func jsonStringAt(payload map[string]any, path string) string {
	v, ok := lookupPath(payload, path)
	if !ok {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// expand 展开 {{var}} 模板。变量按精确名匹配（配置项 key 可能含空格，
// 如 nokvm 的 "Disk Space"），未提供的变量展开为空串。
func expand(tpl string, vars map[string]string) string {
	if !strings.Contains(tpl, "{{") {
		return tpl
	}
	var b bytes.Buffer
	for {
		i := strings.Index(tpl, "{{")
		if i < 0 {
			b.WriteString(tpl)
			break
		}
		j := strings.Index(tpl[i:], "}}")
		if j < 0 {
			b.WriteString(tpl)
			break
		}
		name := strings.TrimSpace(tpl[i+2 : i+j])
		b.WriteString(tpl[:i])
		b.WriteString(vars[name])
		tpl = tpl[i+j+2:]
	}
	return b.String()
}

// varsFromCreate 组装 create 动作的模板变量。
func varsFromCreate(req provider.CreateRequest) map[string]string {
	vars := map[string]string{
		"uid": fmt.Sprint(req.UserID),
	}
	strs := map[string]string{}
	for k, v := range req.Options {
		switch val := v.(type) {
		case string:
			strs[k] = val
		case float64:
			strs[k] = anyToString(val)
		case bool:
			if val {
				strs[k] = "1"
			} else {
				strs[k] = "0"
			}
		}
	}
	for _, k := range []string{"domain", "username", "password", "email"} {
		if v := strs[k]; v != "" {
			vars[k] = v
		}
	}
	// 自定义字段以 cf_<field_key> 引用
	if cf, ok := req.Options["customfields"]; ok {
		switch m := cf.(type) {
		case map[string]string:
			for k, v := range m {
				vars["cf_"+k] = v
			}
		case map[string]any:
			for k, v := range m {
				if sv := anyToString(v); sv != "" {
					vars["cf_"+k] = sv
				}
			}
		}
	}
	// 配置项：configoptions（map）与 opt_ 前缀键都摊进模板变量
	if co, ok := req.Options["configoptions"]; ok {
		switch m := co.(type) {
		case map[string]string:
			for k, v := range m {
				vars["opt_"+k] = v
			}
		case map[string]any:
			for k, v := range m {
				if s := anyToString(v); s != "" {
					vars["opt_"+k] = s
				}
			}
		}
	}
	for k, v := range strs {
		if strings.HasPrefix(k, "opt_") {
			vars[k] = v
		}
	}
	return vars
}

// flattenSelections 把升降级请求里的配置项取值摊平成 key→value。
func flattenSelections(data map[string]any) map[string]string {
	out := map[string]string{}
	if data == nil {
		return out
	}
	if co, ok := data["configoptions"].(map[string]any); ok {
		for k, v := range co {
			if s := anyToString(v); s != "" {
				out[k] = s
			}
		}
	}
	for k, v := range data {
		if strings.HasPrefix(k, "opt_") {
			if s := anyToString(v); s != "" {
				out[k] = s
			}
		}
	}
	return out
}

func anyToString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		if x {
			return "1"
		}
		return "0"
	default:
		return ""
	}
}

func mergeMaps(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// encodeParams 按键排序输出 urlencoded 表单（确定性便于测试）。
func encodeParams(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b bytes.Buffer
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(urlQueryEscape(k))
		b.WriteByte('=')
		b.WriteString(urlQueryEscape(m[k]))
	}
	return b.String()
}

func urlQueryEscape(s string) string {
	const hexDigits = "0123456789ABCDEF"
	var b bytes.Buffer
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			b.WriteByte(c)
		case c == ' ':
			b.WriteByte('+')
		default:
			b.WriteByte('%')
			b.WriteByte(hexDigits[c>>4])
			b.WriteByte(hexDigits[c&0xf])
		}
	}
	return b.String()
}
