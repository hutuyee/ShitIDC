// Package wlkangle 实现对接 kangle 系虚拟主机面板（未来 kangle 高级版）的 Provider。
//
// 协议来自魔方财务的参考模块 public/plugins/servers/wlkanglepro/wlkanglepro.php：
//
//	签名  s = md5(a + token + r)，a 是动作名、r 是 6 位随机数，每次请求现生成。
//	URL   {base}/api/index.php?c=whm&a=<动作>&<参数>r=<r>&s=<s>&json=1
//	成功判据 响应 JSON 的 result == 200。
//	动作  info（连通测试）/ add_vh（开通，init=1）/ add_vh&edit=1（改配）/
//	     update_vh&status=1|0（暂停/恢复）/ del_vh（删除）/ change_password。
//	续费  参考实现的 _Renew 就是解除暂停——到期暂停的服务续费后立即恢复。
//	兼容  kanghostx（CBAP sub_server 的 V10 模块）：面板 API 相同，配置键位是
//	     way + parameter1..16（自定义）/ kl_*（弹性），带宽按 M×128 换算。
package wlkangle

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hutuyee/ShitIDC/internal/provider"
	"github.com/hutuyee/ShitIDC/internal/security"
)

// Config 是 wlkangle Provider 的配置。
type Config struct {
	BaseURL      string `json:"base_url"`
	Token        string `json:"token,omitempty"`
	AllowPrivate bool   `json:"allow_private"`
}

// Client 是与 kangle 面板通信的客户端。
type Client struct {
	cfg  Config
	http *http.Client
	mu   sync.Mutex
	rng  *rand.Rand
}

// New 构造客户端并校验必填配置。token 即面板安全码（accesshash）。
func New(cfg Config) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("wlkangle 需要面板地址 base_url")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("wlkangle 需要安全码 token")
	}
	if err := security.ValidateOutboundURL(cfg.BaseURL, cfg.AllowPrivate); err != nil {
		return nil, err
	}
	return newClient(cfg, security.SafeHTTPClient(cfg.AllowPrivate, 30*time.Second)), nil
}

// NewWithHTTPClient 允许注入 HTTP 客户端（测试用）。
func NewWithHTTPClient(cfg Config, httpClient *http.Client) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("wlkangle 需要面板地址 base_url")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("wlkangle 需要安全码 token")
	}
	if httpClient == nil {
		httpClient = security.SafeHTTPClient(cfg.AllowPrivate, 30*time.Second)
	}
	return newClient(cfg, httpClient), nil
}

func newClient(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// createSign 复刻 wlkanglepro_CreateSign：s = md5(a + token + r)。
func createSign(action, token, r string) string {
	sum := md5.Sum([]byte(action + token + r))
	return hex.EncodeToString(sum[:])
}

// nextR 取 6 位随机数（参考实现 rand(100000, 999999)）。
func (c *Client) nextR() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return fmt.Sprintf("%d", 100000+c.rng.Intn(900000))
}

// call 拼带签名的请求 URL 并解析响应（result==200 为成功）。
// 所有参数（含业务参数）都按参考实现拼在 query 上（file_get_contents GET）。
func (c *Client) call(ctx context.Context, action string, params url.Values) (map[string]any, error) {
	r := c.nextR()
	q := url.Values{"c": {"whm"}, "a": {action}}
	for k := range params {
		q.Set(k, params.Get(k))
	}
	q.Set("r", r)
	q.Set("s", createSign(action, c.cfg.Token, r))
	q.Set("json", "1")
	target := c.cfg.BaseURL + "/api/index.php?" + q.Encode()
	if !c.cfg.AllowPrivate {
		if err := security.ValidateOutboundURL(target, false); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kangle 请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Result int            `json:"result"`
		Msg    string         `json:"msg"`
		Status any            `json:"status"`
		Data   map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("kangle 返回无法解析: %s", truncateWlkLog(body))
	}
	if envelope.Result != 200 {
		return nil, fmt.Errorf("kangle 返回失败(result=%d): %s", envelope.Result, firstNonEmptyWlk(envelope.Msg, truncateWlkLog(body)))
	}
	if envelope.Data == nil {
		envelope.Data = map[string]any{}
	}
	return envelope.Data, nil
}

// TestConnection 调 info 动作（参考实现 TestLink）。
func (c *Client) TestConnection(ctx context.Context) error {
	_, err := c.call(ctx, "info", url.Values{})
	return err
}

// vhName 把开通请求变成一个合法的主机名（kangle 用 name 标识虚拟主机）。
func vhName(req provider.CreateRequest) string {
	raw := strings.ToLower(req.RequestID)
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" {
		name = fmt.Sprintf("vh%d", time.Now().UnixNano()%1000000)
	}
	if len(name) > 32 {
		name = name[:32]
	}
	return name
}

func randomPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 10)
	for i := range b {
		b[i] = alphabet[randIntN(len(alphabet))]
	}
	return string(b)
}

var pwRand = rand.New(rand.NewSource(time.Now().UnixNano()))
var pwMu sync.Mutex

func randIntN(n int) int {
	pwMu.Lock()
	defer pwMu.Unlock()
	return pwRand.Intn(n)
}

// createForm 从配置项拼 add_vh 的参数，两套键位都支持：
// wlkanglepro（type / web_quota / db_quota / flow_limit / cdn ...，product_id 方式只带 ID）
// 与 kanghostx（way / parameter1..16 / kl_*，见 kanghostxForm）。
func createForm(req provider.CreateRequest, name, password string) url.Values {
	cfg := map[string]any{}
	if m, ok := req.Options["configoptions"].(map[string]any); ok {
		cfg = m
	}
	if isKanghostxConfig(cfg) {
		return kanghostxForm(cfg, name, password)
	}
	get := func(k string) string { return configString(cfg, k) }
	form := url.Values{
		"c":      {"whm"},
		"init":   {"1"},
		"name":   {name},
		"passwd": {password},
	}
	// 开通方式：0 自定义配置 / 1 产品ID（弹性配置按自定义处理，见参考实现 else 分支）。
	if get("type") == "1" {
		form.Set("product_id", get("product_id"))
		return form
	}
	for k, def := range map[string]string{
		"cdn": "0", "module": "php", "web_quota": "1024", "db_quota": "1024",
		"db_type": "mysql", "subdir_flag": "0", "max_subdir": "0", "flow_limit": "1024",
		"subdir": "wwwroot", "speed_limit": "0", "ftp": "1", "max_connect": "0",
		"access": "1", "htaccess": "1", "log_file": "1", "log_handle": "1",
		"ssi": "1", "port": "80,443s",
	} {
		form.Set(k, firstNonEmptyWlk(get(k), def))
	}
	if dom := get("domain"); dom != "" {
		form.Set("domain", dom)
	}
	return form
}

// isKanghostxConfig 判断配置键是否来自 kanghostx（CBAP sub_server 的 V10 模块）：
// 它用 way 切换开通方式，资源参数名为 parameter1..parameter16（自定义）
// 或 kl_*（弹性），与 wlkanglepro 的 type / web_quota 体系不重叠。
func isKanghostxConfig(cfg map[string]any) bool {
	if _, ok := cfg["way"]; ok {
		return true
	}
	for k := range cfg {
		if strings.HasPrefix(k, "parameter") || strings.HasPrefix(k, "kl_") {
			return true
		}
	}
	return false
}

// configString 读一个配置值并转成字符串（等价参考实现的 $params['configoptions'][key]）。
func configString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return fmt.Sprint(v)
}

// kanghostxForm 复刻 kanghostx_CreateAccount / _ChangePackage 的拼参逻辑：
// way=0 自定义（parameterN）、way=1 弹性（kl_*）；空值不下发（对齐 PHP 的
// isset && !empty）。带宽字段单位是 M、面板要 KB，按 PHP 的 $v * 128 换算。
//
// 与参考实现的一处差异（有意）：_ChangePackage 不区分 way、固定读 kl_*，
// 这里跟随 way 读对应键位，避免自定义产品升降级时丢参数。
func kanghostxForm(cfg map[string]any, name, password string) url.Values {
	form := url.Values{
		"c":       {"whm"},
		"init":    {"1"},
		"name":    {name},
		"passwd":  {password},
		"module":  {"php"},
		"db_type": {"mysql"},
	}
	// 空字符串不下发；「0」按配置项语义原样下发。
	set := func(key, value string) {
		if value != "" {
			form.Set(key, value)
		}
	}
	// 两种开通方式共用的固定键：parameter1/2/5/16 与 ftp。
	set("cdn", configString(cfg, "parameter1"))
	set("subdir_flag", configString(cfg, "parameter2"))
	set("subdir", configString(cfg, "parameter5"))
	set("ftp", configString(cfg, "ftp"))
	set("port", configString(cfg, "parameter16"))
	way := configString(cfg, "way")
	if way == "1" || strings.EqualFold(way, "true") || way == "是" {
		// 弹性配置：资源参数由用户下单时选择（kl_* 键）。
		set("web_quota", configString(cfg, "kl_site"))
		set("db_quota", configString(cfg, "kl_sql"))
		set("domain", configString(cfg, "kl_domain"))
		set("max_subdir", configString(cfg, "kl_zi"))
		set("flow_limit", configString(cfg, "kl_flow"))
		set("speed_limit", speedLimitKb(configString(cfg, "kl_speed")))
		set("max_connect", configString(cfg, "kl_connect"))
		set("access", configString(cfg, "kl_access"))
		set("htaccess", configString(cfg, "kl_htaccess"))
		set("log_file", configString(cfg, "kl_log_file"))
		set("log_handle", configString(cfg, "kl_log_handle"))
		set("ssi", configString(cfg, "kl_ssi"))
		return form
	}
	// 自定义配置（way=0）：parameterN 键位。
	set("domain", configString(cfg, "parameter3"))
	set("max_subdir", configString(cfg, "parameter4"))
	set("web_quota", configString(cfg, "parameter6"))
	set("db_quota", configString(cfg, "parameter7"))
	set("flow_limit", configString(cfg, "parameter8"))
	set("speed_limit", speedLimitKb(configString(cfg, "parameter9")))
	set("max_connect", configString(cfg, "parameter10"))
	set("access", configString(cfg, "parameter11"))
	set("log_file", configString(cfg, "parameter12"))
	set("log_handle", configString(cfg, "parameter13"))
	set("ssi", configString(cfg, "parameter14"))
	set("htaccess", configString(cfg, "parameter15"))
	return form
}

// speedLimitKb 把 kanghostx 的带宽值（M）换算成面板要的 KB 数（×128，
// 对应 PHP 的 $v * 128）；非数字原样返回。
func speedLimitKb(value string) string {
	if value == "" {
		return ""
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return value
	}
	return strconv.Itoa(n * 128)
}

// Create 开通一个虚拟主机（kangle 的 add_vh）。
func (c *Client) Create(ctx context.Context, req provider.CreateRequest) (*provider.Instance, error) {
	name := vhName(req)
	password := randomPassword()
	data, err := c.call(ctx, "add_vh", createForm(req, name, password))
	if err != nil {
		return nil, err
	}
	return &provider.Instance{
		ID: name,
		Data: map[string]any{
			"name":     name,
			"password": password,
			"panel":    c.cfg.BaseURL,
			"raw":      data,
		},
	}, nil
}

// Suspend 停用主机（update_vh status=1）。
func (c *Client) Suspend(ctx context.Context, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return errors.New("缺少 kangle 主机名")
	}
	_, err := c.call(ctx, "update_vh", url.Values{"name": {ref}, "status": {"1"}})
	return err
}

// Unsuspend 启用主机（update_vh status=0）。
func (c *Client) Unsuspend(ctx context.Context, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return errors.New("缺少 kangle 主机名")
	}
	_, err := c.call(ctx, "update_vh", url.Values{"name": {ref}, "status": {"0"}})
	return err
}

// Terminate 删除主机（del_vh）。
func (c *Client) Terminate(ctx context.Context, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return errors.New("缺少 kangle 主机名")
	}
	_, err := c.call(ctx, "del_vh", url.Values{"name": {ref}})
	return err
}

// Renew 与参考实现一致：续费即解除暂停（到期暂停的服务恢复运行）。
func (c *Client) Renew(ctx context.Context, r provider.RenewRequest) error {
	return c.Unsuspend(ctx, r.InstanceID)
}

// ChangePackage 重新提交 add_vh（edit=1）覆盖配额参数，与参考实现 _ChangePackage 一致。
func (c *Client) ChangePackage(ctx context.Context, r provider.ChangePackageRequest) error {
	if strings.TrimSpace(r.InstanceID) == "" {
		return errors.New("缺少 kangle 主机名")
	}
	form := createForm(provider.CreateRequest{Options: map[string]any{"configoptions": r.SelectionsJSON}}, strings.TrimSpace(r.InstanceID), "")
	form.Set("edit", "1")
	form.Set("passwd", "")
	// edit 模式沿用现有密码：passwd 留空时 kangle 保持原值。
	_, err := c.call(ctx, "add_vh", form)
	return err
}

func firstNonEmptyWlk(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func truncateWlkLog(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
