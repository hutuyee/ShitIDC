// Package baota implements the resource Provider interface for 宝塔面板 (BaoTa / aaPanel).
//
// 协议来自魔方财务 3.7.6 的明文参考模块
// public/plugins/servers/bthosts/bthosts.php（该模块就是对接宝塔类面板的），
// 签名算法是：把 time、random、token 三个值按字符串排序后拼接，取 md5 再转大写；
// token 本身不随请求发送，只参与摘要。请求为 x-www-form-urlencoded POST。
//
// Provider config（providers.config JSONB）：site_type / php_version / site_path /
// db / allow_private。providers.secret_encrypted 保存面板 API 密钥。
package baota

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

// Config 是宝塔 Provider 的配置。
type Config struct {
	BaseURL      string `json:"base_url"`
	APIKey       string `json:"api_key,omitempty"`
	SiteType     string `json:"site_type"`
	PHPVersion   string `json:"php_version"`
	SitePath     string `json:"site_path"`
	CreateDB     bool   `json:"db"`
	AllowPrivate bool   `json:"allow_private"`
}

// Client 是与宝塔面板通信的客户端。
type Client struct {
	cfg  Config
	http *http.Client
	mu   sync.Mutex
	rng  *rand.Rand
}

// New 构造客户端并校验必填配置。
func New(cfg Config) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("宝塔需要面板地址 base_url")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("宝塔需要面板 API 密钥")
	}
	if err := security.ValidateOutboundURL(cfg.BaseURL, cfg.AllowPrivate); err != nil {
		return nil, err
	}
	return newClient(cfg, security.SafeHTTPClient(cfg.AllowPrivate, 30*time.Second)), nil
}

// NewWithHTTPClient 与 New 相同，但允许注入 HTTP 客户端。
// 生产代码用 New 即可；这个注入点是为了测试——SSRF 防护会把回环地址挡在门外，
// 所以针对面板协议的测试需要用一个假的 RoundTripper 来观察真正发出的请求。
func NewWithHTTPClient(cfg Config, httpClient *http.Client) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("宝塔需要面板地址 base_url")
	}
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("宝塔需要面板 API 密钥")
	}
	if httpClient == nil {
		httpClient = security.SafeHTTPClient(cfg.AllowPrivate, 30*time.Second)
	}
	return newClient(cfg, httpClient), nil
}

// newClient 套用默认值并组装客户端。
func newClient(cfg Config, httpClient *http.Client) *Client {
	if cfg.SitePath == "" {
		cfg.SitePath = "/www/wwwroot"
	}
	if cfg.SiteType == "" {
		cfg.SiteType = "PHP"
	}
	return &Client{
		cfg:  cfg,
		http: httpClient,
		rng:  rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

// sign 复刻宝塔模块的签名算法。
func sign(timestamp int64, random int64, token string) string {
	values := []string{strconv.FormatInt(timestamp, 10), strconv.FormatInt(random, 10), token}
	sort.Strings(values)
	sum := md5.Sum([]byte(strings.Join(values, "")))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

// nextRandom 线程安全地取一个随机数（宝塔要求每次请求都不同）。
func (c *Client) nextRandom() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rng.Int63n(1 << 31)
}

// call 发一个带签名的 POST 请求，返回解析后的 data 字段。
func (c *Client) call(ctx context.Context, path string, payload map[string]string) (map[string]any, error) {
	ts := time.Now().Unix()
	rnd := c.nextRandom()
	form := url.Values{}
	form.Set("time", strconv.FormatInt(ts, 10))
	form.Set("random", strconv.FormatInt(rnd, 10))
	form.Set("signature", sign(ts, rnd, c.cfg.APIKey))
	for k, v := range payload {
		form.Set(k, v)
	}
	target := c.cfg.BaseURL + "/" + strings.TrimLeft(path, "/")
	// SSRF 防护默认开启；只有显式配置 allow_private（面板确实部署在内网）才放行私网地址。
	if !c.cfg.AllowPrivate {
		if err := security.ValidateOutboundURL(target, false); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("宝塔请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("宝塔 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var envelope struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("宝塔返回无法解析: %w", err)
	}
	// 宝塔不同版本用 code=0 或 1 表示成功，两种都接受。
	if envelope.Code != 0 && envelope.Code != 1 {
		msg := envelope.Msg
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		return nil, fmt.Errorf("宝塔返回失败(code=%d): %s", envelope.Code, msg)
	}
	if envelope.Data == nil {
		envelope.Data = map[string]any{}
	}
	return envelope.Data, nil
}

// TestConnection 调用面板的取站点列表接口，能返回即视为连通。
func (c *Client) TestConnection(ctx context.Context) error {
	_, err := c.call(ctx, "sites?action=GetSiteList", map[string]string{"page": "1", "limit": "1"})
	return err
}

// siteName 把 CreateRequest 变成一个合法的站点名（宝塔只接受字母数字与点）。
func siteName(req provider.CreateRequest) string {
	raw := strings.ToLower(strings.TrimSpace(req.RequestID))
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if len(name) > 12 {
		name = name[:12]
	}
	if name == "" {
		name = "site" + strconv.FormatInt(time.Now().UnixNano()%1000000, 10)
	}
	return name + ".shitidc.local"
}

// randomPassword 生成一个满足宝塔复杂度要求的初始密码。
func randomPassword() string {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789!@#"
	b := make([]byte, 16)
	for i := range b {
		b[i] = alphabet[rngInt(len(alphabet))]
	}
	return string(b)
}

var passwordRand = rand.New(rand.NewSource(time.Now().UnixNano()))
var passwordMu sync.Mutex

func rngInt(n int) int {
	passwordMu.Lock()
	defer passwordMu.Unlock()
	return passwordRand.Intn(n)
}

// Create 在面板上创建一个站点（宝塔的"开通"就是建站 + 可选建库）。
// 返回的 Instance.Data 带上用户名与密码，写回服务实例供用户查看。
func (c *Client) Create(ctx context.Context, req provider.CreateRequest) (*provider.Instance, error) {
	name := siteName(req)
	password := randomPassword()
	payload := map[string]string{
		"webname":  name,
		"type":     c.cfg.SiteType,
		"port":     "80",
		"ps":       truncateRunes("ShitIDC 自动开通", 100),
		"path":     strings.TrimRight(c.cfg.SitePath, "/") + "/" + strings.Split(name, ".")[0],
		"type_id":  "0",
		"version":  "0",
		"set_ssl":  "0",
		"ftp":      "false",
		"sql":      strconv.FormatBool(c.cfg.CreateDB),
		"codeing":  "utf8",
		"datauser": strings.Split(name, ".")[0],
		"datapass": password,
	}
	if strings.TrimSpace(c.cfg.PHPVersion) != "" {
		payload["version"] = c.cfg.PHPVersion
	}
	data, err := c.call(ctx, "sites?action=AddSite", payload)
	if err != nil {
		return nil, err
	}
	// 宝塔把站点 ID 放在 data.siteId（部分版本是 id）。
	id := firstStringValue(data, "siteId", "site_id", "id")
	if id == "" {
		return nil, fmt.Errorf("宝塔未返回站点 ID，无法继续管理该站点")
	}
	return &provider.Instance{
		ID: id,
		Data: map[string]any{
			"site_id":  id,
			"domain":   name,
			"username": strings.Split(name, ".")[0],
			"password": password,
			"panel":    c.cfg.BaseURL,
		},
	}, nil
}

// firstStringValue 从宝塔返回的 data 里按多个候选键取字符串。
func firstStringValue(data map[string]any, keys ...string) string {
	for _, k := range keys {
		v, ok := data[k]
		if !ok || v == nil {
			continue
		}
		switch t := v.(type) {
		case string:
			if strings.TrimSpace(t) != "" {
				return strings.TrimSpace(t)
			}
		case float64:
			return strconv.FormatInt(int64(t), 10)
		default:
			s := fmt.Sprint(t)
			if s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

// truncateRunes 按字符截断，避免把中文截坏。
func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// setSiteStatus 是停用/启用站点的公共实现。
func (c *Client) setSiteStatus(ctx context.Context, ref, action string) error {
	id := strings.TrimSpace(ref)
	if id == "" {
		return fmt.Errorf("缺少宝塔站点 ID")
	}
	_, err := c.call(ctx, "sites?action="+action, map[string]string{"id": id})
	return err
}

// Suspend 停用站点（宝塔的 StopSite）。
func (c *Client) Suspend(ctx context.Context, ref string) error {
	return c.setSiteStatus(ctx, ref, "StopSite")
}

// Unsuspend 重新启用站点。
func (c *Client) Unsuspend(ctx context.Context, ref string) error {
	return c.setSiteStatus(ctx, ref, "StartSite")
}

// Terminate 删除站点（含数据库与目录）。
func (c *Client) Terminate(ctx context.Context, ref string) error {
	id := strings.TrimSpace(ref)
	if id == "" {
		return fmt.Errorf("缺少宝塔站点 ID")
	}
	_, err := c.call(ctx, "sites?action=DeleteSite", map[string]string{
		"id":   id,
		"web":  "1",
		"ftp":  "1",
		"sql":  "1",
		"path": "1",
	})
	return err
}

// Renew 对宝塔站点是空操作：到期时间由 ShitIDC 自己记账，面板侧无需改动。
func (c *Client) Renew(context.Context, string) error { return nil }

// ChangePackage 说明宝塔站点不支持在线改配（套餐由面板侧资源决定），
// 交给调用方做本地记账即可。
func (c *Client) ChangePackage(context.Context, provider.ChangePackageRequest) error {
	return provider.ErrChangePackageUnsupported
}

// SiteUsage 是宝塔站点的资源占用。
type SiteUsage struct {
	DiskMB int64 `json:"disk_mb"`
	BWGB   int64 `json:"bw_gb"`
}

// FetchUsage 读取站点的磁盘与流量占用，供按量计费使用。
// 宝塔不同版本字段名不一致，这里做兼容读取；读不到就返回 0，由调用方决定是否上报。
func (c *Client) FetchUsage(ctx context.Context, ref string) (SiteUsage, error) {
	id := strings.TrimSpace(ref)
	if id == "" {
		return SiteUsage{}, fmt.Errorf("缺少宝塔站点 ID")
	}
	data, err := c.call(ctx, "sites?action=GetSiteInfo", map[string]string{"id": id})
	if err != nil {
		return SiteUsage{}, err
	}
	var u SiteUsage
	if mb := firstStringValue(data, "size_mb", "disk_mb", "site_size"); mb != "" {
		if f, err := strconv.ParseFloat(mb, 64); err == nil {
			u.DiskMB = int64(f)
		}
	}
	if gb := firstStringValue(data, "bw_gb", "flow_gb", "bandwidth_gb"); gb != "" {
		if f, err := strconv.ParseFloat(gb, 64); err == nil {
			u.BWGB = int64(f)
		}
	}
	return u, nil
}
