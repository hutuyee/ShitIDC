// Package nokvm 实现对接 NOKVM 虚拟化面板的 Provider。
//
// 协议来自魔方财务的明文参考模块 public/plugins/servers/nokvm/nokvm.php：
//
//	签名  query 带 time / random / signature；signature =
//	     UPPER(MD5(把 [time值, random值, token值] 按字符串排序后直接拼接))
//	     —— 注意排序的是**值**而不是键名，token 只参与摘要不发送。
//	请求  POST/PUT 用 form 体；GET/DELETE 参数全在 query。成功判据 code == 0。
//	端点  开通 POST /api/virtual，暂停 GET /api/virtual_pause/{id}，
//	     恢复 GET /api/virtual_restore_pause/{id}，删除 DELETE /api/virtual/{id}，
//	     改配 PUT /api/virtual/{id}，连通测试 GET /api/area。
//
// 与参考实现一致的两点：开通的 expire_time 固定 2999-01-01（到期由本系统回收，
// nokvm 没有 _Renew）；Windows 镜像的管理员用户名是 administrator，其余 root。
package nokvm

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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hutuyee/ShitIDC/internal/provider"
	"github.com/hutuyee/ShitIDC/internal/security"
)

// Config 是 nokvm Provider 的配置。
type Config struct {
	BaseURL      string `json:"base_url"`
	Token        string `json:"token,omitempty"`
	AllowPrivate bool   `json:"allow_private"`
}

// Client 是与 NOKVM 面板通信的客户端。
type Client struct {
	cfg  Config
	http *http.Client
	mu   sync.Mutex
	rng  *rand.Rand
}

// New 构造客户端并校验必填配置。token 即面板 API 密钥（providers.secret）。
func New(cfg Config) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("nokvm 需要面板地址 base_url")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("nokvm 需要 API token")
	}
	if err := security.ValidateOutboundURL(cfg.BaseURL, cfg.AllowPrivate); err != nil {
		return nil, err
	}
	return newClient(cfg, security.SafeHTTPClient(cfg.AllowPrivate, 30*time.Second)), nil
}

// NewWithHTTPClient 允许注入 HTTP 客户端（测试用，SSRF 防护会挡回环地址）。
func NewWithHTTPClient(cfg Config, httpClient *http.Client) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("nokvm 需要面板地址 base_url")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("nokvm 需要 API token")
	}
	if httpClient == nil {
		httpClient = security.SafeHTTPClient(cfg.AllowPrivate, 30*time.Second)
	}
	return newClient(cfg, httpClient), nil
}

func newClient(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// sign 复刻 nokvm_CreateSign：值排序后直接拼接，MD5 大写。
// PHP 的 sort($data, SORT_STRING) 排序的是数组的**值**，这里必须一致。
func sign(timestamp, randomStr, token string) string {
	values := []string{timestamp, randomStr, token}
	sort.Strings(values)
	sum := md5.Sum([]byte(strings.Join(values, "")))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

const randAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"

// nextRandom 生成 6 位随机串（与参考的 randStr(6) 同字符集）。
func (c *Client) nextRandom() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := make([]byte, 6)
	for i := range b {
		b[i] = randAlphabet[c.rng.Intn(len(randAlphabet))]
	}
	return string(b)
}

// call 发一个带签名的请求并解析响应（code==0 为成功）。
// query 恒只有 time/random/signature 三元组；业务参数 POST/PUT 走 form 体
// （GET/DELETE 场景参考实现也不带业务参数）。
func (c *Client) call(ctx context.Context, method, path string, form url.Values) (map[string]any, error) {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	rnd := c.nextRandom()
	q := url.Values{
		"time":      {ts},
		"random":    {rnd},
		"signature": {sign(ts, rnd, c.cfg.Token)},
	}
	target := c.cfg.BaseURL + path + "?" + q.Encode()
	if !c.cfg.AllowPrivate {
		if err := security.ValidateOutboundURL(target, false); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "WHMCS")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("nokvm 请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var envelope struct {
		Code    int            `json:"code"`
		Message string         `json:"message"`
		Data    map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("nokvm 返回无法解析: %s", truncateNokvmLog(body))
	}
	if envelope.Code != 0 {
		return nil, fmt.Errorf("nokvm 返回失败(code=%d): %s", envelope.Code, firstNonEmptyNokvm(envelope.Message, truncateNokvmLog(body)))
	}
	if envelope.Data == nil {
		envelope.Data = map[string]any{}
	}
	return envelope.Data, nil
}

// TestConnection 调 /api/area（参考实现 TestLink 用的端点）。
func (c *Client) TestConnection(ctx context.Context) error {
	_, err := c.call(ctx, http.MethodGet, "/api/area", url.Values{})
	return err
}

// configMap 从开通上下文取配置项（对应魔方 $params['configoptions']，
// 键名沿用 nokvm_ConfigOptions 的 key：CPU / Memory / Disk Space / os ...）。
func configMap(req provider.CreateRequest) map[string]any {
	if m, ok := req.Options["configoptions"].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// cfgString / cfgInt 宽容读取配置项：前端传来的值可能是字符串或数字。
func cfgString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatInt(int64(t), 10)
	default:
		return fmt.Sprint(t)
	}
}

func cfgInt(m map[string]any, key string, def int64) int64 {
	s := cfgString(m, key)
	if s == "" {
		return def
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return n
	}
	return def
}

// randomPassword 生成 8 位初始密码（参考实现 sys_pwd / vnc_pwd 都是 8 位）。
func randomPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
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

// Create 开通一台虚拟机。
// 返回的 Instance.Data 带上 IP、用户名与密码，写回服务实例供用户查看。
func (c *Client) Create(ctx context.Context, req provider.CreateRequest) (*provider.Instance, error) {
	cfg := configMap(req)
	sysPwd, vncPwd := randomPassword(), randomPassword()
	form := url.Values{
		"users_id":       {strconv.FormatInt(req.UserID, 10)},
		"username":       {firstNonEmptyNokvm(cfgString(req.Options, "email"), "u"+strconv.FormatInt(req.UserID, 10))},
		"core":           {cfgString(cfg, "CPU")},
		"cpu_mode":       {firstNonEmptyNokvm(cfgString(cfg, "cpu_mode"), "1")},
		"memory":         {cfgString(cfg, "Memory")},
		"data_disk_size": {cfgString(cfg, "Disk Space")},
		"net_out":        {firstNonEmptyNokvm(cfgString(cfg, "net_out"), cfgString(cfg, "Network Speed"))},
		"net_in":         {firstNonEmptyNokvm(cfgString(cfg, "net_in"), cfgString(cfg, "Network Speed"))},
		"snapshoot":      {cfgString(cfg, "Snapshot")},
		"backups":        {cfgString(cfg, "Backups")},
		"templates_id":   {cfgString(cfg, "os")},
		"sys_pwd":        {sysPwd},
		"vnc_pwd":        {vncPwd},
		"expire_time":    {"2999-01-01 00:00:00"},
		"ip_num":         {cfgString(cfg, "Extra IP Address")},
		"flow_limit":     {cfgString(cfg, "flow_limit")},
		"nat_acl_limit":  {cfgString(cfg, "nat_acl_limit")},
		"nat_web_limit":  {cfgString(cfg, "nat_web_limit")},
	}
	if nodes := cfgString(cfg, "nodes_id"); nodes != "" {
		form.Set("nodes_id", nodes)
	}
	if area := cfgString(cfg, "Location"); area != "" {
		form.Set("areas_id", area)
	}
	data, err := c.call(ctx, http.MethodPost, "/api/virtual", form)
	if err != nil {
		return nil, err
	}
	id := firstStringValue(data, "id")
	if id == "" {
		return nil, errors.New("nokvm 未返回虚拟机 ID，无法继续管理")
	}
	mainIP, extraIPs := splitIPs(data)
	osName := strings.ToLower(cfgString(cfg, "os"))
	username := "root"
	if strings.Contains(osName, "win") {
		username = "administrator"
	}
	return &provider.Instance{
		ID: id,
		Data: map[string]any{
			"vserverid":    id,
			"name":         firstStringValue(data, "name"),
			"main_ip":      mainIP,
			"assigned_ips": extraIPs,
			"username":     username,
			"password":     sysPwd,
			"vnc_password": vncPwd,
		},
	}, nil
}

// splitIPs 按 ip_address_id 把主 IP 与附加 IP 分开（参考实现的逻辑）。
func splitIPs(data map[string]any) (string, []string) {
	mainID := firstStringValue(data, "ip_address_id")
	mainIP := ""
	var extra []string
	ips, _ := data["public_ip"].([]any)
	for _, item := range ips {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		ip := firstStringValue(m, "ip")
		if ip == "" {
			continue
		}
		if firstStringValue(m, "id") == mainID {
			mainIP = ip
		} else {
			extra = append(extra, ip)
		}
	}
	return mainIP, extra
}

// Suspend 暂停虚拟机。
func (c *Client) Suspend(ctx context.Context, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return errors.New("缺少 nokvm 虚拟机 ID")
	}
	_, err := c.call(ctx, http.MethodGet, "/api/virtual_pause/"+strings.TrimSpace(ref), url.Values{})
	return err
}

// Unsuspend 解除暂停。
func (c *Client) Unsuspend(ctx context.Context, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return errors.New("缺少 nokvm 虚拟机 ID")
	}
	_, err := c.call(ctx, http.MethodGet, "/api/virtual_restore_pause/"+strings.TrimSpace(ref), url.Values{})
	return err
}

// Terminate 删除虚拟机。
func (c *Client) Terminate(ctx context.Context, ref string) error {
	if strings.TrimSpace(ref) == "" {
		return errors.New("缺少 nokvm 虚拟机 ID")
	}
	_, err := c.call(ctx, http.MethodDelete, "/api/virtual/"+strings.TrimSpace(ref), url.Values{})
	return err
}

// Renew 是空操作：参考实现没有 _Renew，开通即设 2999 到期，回收由本系统调度。
func (c *Client) Renew(context.Context, provider.RenewRequest) error { return nil }

// ChangePackage 调 PUT /api/virtual/{id} 改配（CPU / 内存 / 硬盘 / 带宽 / 流量 / IP 数）。
// 只提交 SelectionsJSON 里出现的键（与参考实现 configoptions_upgrade 的语义一致）。
func (c *Client) ChangePackage(ctx context.Context, r provider.ChangePackageRequest) error {
	if strings.TrimSpace(r.InstanceID) == "" {
		return errors.New("缺少 nokvm 虚拟机 ID")
	}
	form := url.Values{}
	changed := map[string]bool{}
	for k := range r.SelectionsJSON {
		changed[k] = true
	}
	sel := func(key string) string { return cfgString(r.SelectionsJSON, key) }
	if changed["CPU"] {
		form.Set("core", sel("CPU"))
	}
	if changed["Memory"] {
		form.Set("memory", sel("Memory"))
	}
	if changed["Disk Space"] {
		form.Set("data_disk_size", sel("Disk Space"))
	}
	if changed["Network Speed"] || changed["net_out"] {
		form.Set("net_out", firstNonEmptyNokvm(sel("net_out"), sel("Network Speed")))
	}
	if changed["Network Speed"] || changed["net_in"] {
		form.Set("net_in", firstNonEmptyNokvm(sel("net_in"), sel("Network Speed")))
	}
	if changed["flow_limit"] {
		form.Set("flow_limit", sel("flow_limit"))
	}
	if changed["Extra IP Address"] {
		form.Set("ip_num", sel("Extra IP Address"))
	}
	if len(form) == 0 {
		return provider.ErrChangePackageUnsupported
	}
	_, err := c.call(ctx, http.MethodPut, "/api/virtual/"+strings.TrimSpace(r.InstanceID), form)
	return err
}

// firstStringValue 宽容读取返回字段。
func firstStringValue(data map[string]any, key string) string {
	v, ok := data[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatInt(int64(t), 10)
	default:
		return fmt.Sprint(t)
	}
}

func firstNonEmptyNokvm(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func truncateNokvmLog(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
