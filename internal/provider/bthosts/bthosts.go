// Package bthosts 实现对接「Bthost 主机系统」的 Provider —— 魔方财务随包发布的
// servers/bthosts（btHost 对接模块，APIVersion 1.7.1）与 CBAP sub_server/bthostx
// （宝塔虚拟主机 Bthost 模块 V10 版）共用同一套上游 API：
//
//	签名  strtoupper(md5(implode(sort([time, random, accesshash]))))，token 不随请求发送。
//	地址  {base}/api/vhost/*：GET /index 探活；POST /user_create + /host_build 开通、
//	      /host_locked 暂停、/host_start 启用、/host_recycle 删除（进回收站）、
//	      /host_endtime 改到期时间、/host_edit 改配置、/host_update 套餐切换、
//	      /host_speed 与 /host_speedoff 限速、/host_recovery 回收站恢复。
//	判据  响应 JSON 的 code == 1；业务对象在 data 里（开通返回 data.site.id）。
//
// 配置键同时兼容两个版本：
//   - 经典 bthosts：type（0 自定义 / 1 套餐 / 2 弹性）、plans_id、site_max、sql_max、
//     flow_max、domain_num、perserver、limit_rate、sub_bind …；
//   - V10 bthostx：way（0 自定义 / 1 弹性）+ parameter1..20，弹性档用 bt_site / bt_sql /
//     bt_domain / bt_flow / bt_webback / bt_sqlback / bt_ipnum / bt_perserver / bt_limit。
//
// 单位换算按各自模块口径：V10 的 parameter14 / bt_flow 是 G（×1024 到 MB）、
// parameter15 / bt_limit 是 MB/s（×1024/8 到 KB/s）；经典键位原值直达上游。
//
// 与参考实现的差异（有意）：_Renew 里 recovery / unsuspend 是尽最大努力、失败不阻断，
// 但 host_endtime 失败时返回错误（bthosts 的「续费失败」也返回 success，调度层会误判成功）。
package bthosts

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

// Config 是 Bthost Provider 的配置。
type Config struct {
	BaseURL      string `json:"base_url"`
	Token        string `json:"token,omitempty"`
	AllowPrivate bool   `json:"allow_private"`
}

// Client 是与 Bthost 主机系统通信的客户端。
type Client struct {
	cfg  Config
	http *http.Client
	mu   sync.Mutex
	rng  *rand.Rand
}

// New 构造客户端并校验必填配置。token 即通讯密钥（accesshash）。
func New(cfg Config) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("bthosts 需要面板地址 base_url")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("bthosts 需要通讯密钥 token")
	}
	if err := security.ValidateOutboundURL(cfg.BaseURL, cfg.AllowPrivate); err != nil {
		return nil, err
	}
	return newClient(cfg, security.SafeHTTPClient(cfg.AllowPrivate, 30*time.Second)), nil
}

// NewWithHTTPClient 与 New 相同，但允许注入 HTTP 客户端（测试用）。
func NewWithHTTPClient(cfg Config, httpClient *http.Client) (*Client, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if cfg.BaseURL == "" {
		return nil, errors.New("bthosts 需要面板地址 base_url")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return nil, errors.New("bthosts 需要通讯密钥 token")
	}
	if httpClient == nil {
		httpClient = security.SafeHTTPClient(cfg.AllowPrivate, 30*time.Second)
	}
	return newClient(cfg, httpClient), nil
}

func newClient(cfg Config, httpClient *http.Client) *Client {
	return &Client{cfg: cfg, http: httpClient, rng: rand.New(rand.NewSource(time.Now().UnixNano()))}
}

// sign 复刻 bthosts_/bthostx_CreateSign：time、random、token 按字符串排序拼接后 md5 转大写。
func sign(timestamp int64, random int64, token string) string {
	values := []string{strconv.FormatInt(timestamp, 10), strconv.FormatInt(random, 10), token}
	sort.Strings(values)
	sum := md5.Sum([]byte(strings.Join(values, "")))
	return strings.ToUpper(hex.EncodeToString(sum[:]))
}

func (c *Client) nextRandom() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rng.Int63n(1 << 31)
}

// signedParams 生成 time / random / signature 三个签名参数。
func (c *Client) signedParams() url.Values {
	ts := time.Now().Unix()
	rnd := c.nextRandom()
	params := url.Values{}
	params.Set("time", strconv.FormatInt(ts, 10))
	params.Set("random", strconv.FormatInt(rnd, 10))
	params.Set("signature", sign(ts, rnd, c.cfg.Token))
	return params
}

// call 发一个带签名的 POST 表单请求，返回解析后的 data。
func (c *Client) call(ctx context.Context, path string, payload map[string]string) (map[string]any, error) {
	params := c.signedParams()
	for k, v := range payload {
		params.Set(k, v)
	}
	target := c.cfg.BaseURL + path
	if !c.cfg.AllowPrivate {
		if err := security.ValidateOutboundURL(target, false); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("bthost 请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("bthost 返回 HTTP %d: %s", resp.StatusCode, truncateLog(body))
	}
	return parseEnvelope(body)
}

// ping 探活：TestLink 走 GET /api/vhost/index，签名参数放 query。
func (c *Client) ping(ctx context.Context) error {
	target := c.cfg.BaseURL + "/api/vhost/index?" + c.signedParams().Encode()
	if !c.cfg.AllowPrivate {
		if err := security.ValidateOutboundURL(target, false); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("bthost 请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	_, err = parseEnvelope(body)
	return err
}

// parseEnvelope 解析统一响应：code == 1 为成功，业务数据在 data。
func parseEnvelope(body []byte) (map[string]any, error) {
	var env struct {
		Code int            `json:"code"`
		Msg  string         `json:"msg"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("bthost 返回无法解析: %s", truncateLog(body))
	}
	if env.Code != 1 {
		return nil, fmt.Errorf("bthost 返回失败(code=%d): %s", env.Code, firstNonEmpty(env.Msg, truncateLog(body)))
	}
	if env.Data == nil {
		env.Data = map[string]any{}
	}
	return env.Data, nil
}

// TestConnection 对应模块的 TestLink。
func (c *Client) TestConnection(ctx context.Context) error {
	return c.ping(ctx)
}

// siteName 把开通请求变成一个合法的上游用户名（字母数字）。
func siteName(req provider.CreateRequest) string {
	raw := strings.ToLower(strings.TrimSpace(req.RequestID))
	var b strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if name == "" {
		name = fmt.Sprintf("host%d", time.Now().UnixNano()%1000000)
	}
	if len(name) > 32 {
		name = name[:32]
	}
	return name
}

var passwordRand = rand.New(rand.NewSource(time.Now().UnixNano()))
var passwordMu sync.Mutex

func randomPassword() string {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 10)
	passwordMu.Lock()
	defer passwordMu.Unlock()
	for i := range b {
		b[i] = alphabet[passwordRand.Intn(len(alphabet))]
	}
	return string(b)
}

// configMap 取商品配置项（魔方 configoptions）。
func configMap(req provider.CreateRequest) map[string]any {
	if m, ok := req.Options["configoptions"].(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

// configString 读一个配置值并转成字符串。
func configString(cfg map[string]any, key string) string {
	v, ok := cfg[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

// isV10 判断配置键是否来自 V10 版（bthostx）：way 开关或 parameter*/bt_* 键。
func isV10(cfg map[string]any) bool {
	if _, ok := cfg["way"]; ok {
		return true
	}
	for k := range cfg {
		if strings.HasPrefix(k, "parameter") || strings.HasPrefix(k, "bt_") {
			return true
		}
	}
	return false
}

// isElastic 判断 V10 的 way 是否为弹性档。
func isElastic(cfg map[string]any) bool {
	way := configString(cfg, "way")
	return way == "1" || strings.EqualFold(way, "true") || way == "是"
}

// scaleInt 把数字字符串乘以系数；非数字原样返回。
func scaleInt(value string, mul int) string {
	if value == "" {
		return ""
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return value
	}
	return strconv.Itoa(n * mul)
}

// buildCreateForm 把商品配置翻译成 host_build 的表单（不含 user_id / endtime）。
func buildCreateForm(cfg map[string]any) url.Values {
	form := url.Values{}
	if isV10(cfg) {
		// parameterN 中两档共用的基础项（对应 V10 的 _CreateAccount 两分支都读的键）。
		shared := map[string]string{
			"parameter1": "pack[ftp]", "parameter2": "pack[sub_bind]",
			"parameter3": "pack[domain_audit]", "parameter4": "pack[session]",
			"parameter5": "pack[vsftpd]", "parameter6": "sort_id",
			"parameter7": "pack[domainpools_id]", "parameter8": "pack[ippools_id]",
			"parameter9": "pack[sql]", "parameter10": "pack[phpver]",
			"parameter11": "pack[port]",
		}
		for src, dst := range shared {
			if v := configString(cfg, src); v != "" {
				form.Set(dst, v)
			}
		}
		pairs := map[string]string{
			"parameter12": "pack[site_max]", "parameter13": "pack[sql_max]",
			"parameter14": "pack[flow_max]", "parameter15": "pack[limit_rate]",
			"parameter16": "pack[perserver]", "parameter17": "pack[domain_num]",
			"parameter18": "pack[web_back_num]", "parameter19": "pack[sql_back_num]",
			"parameter20": "pack[ip_num]",
		}
		if isElastic(cfg) {
			// 弹性档的资源项来自 bt_*；parameter12..20 在参考实现的弹性分支里不读。
			pairs = map[string]string{
				"bt_site": "pack[site_max]", "bt_sql": "pack[sql_max]",
				"bt_domain": "pack[domain_num]", "bt_flow": "pack[flow_max]",
				"bt_webback": "pack[web_back_num]", "bt_sqlback": "pack[sql_back_num]",
				"bt_ipnum": "pack[ip_num]", "bt_perserver": "pack[perserver]",
				"bt_limit": "pack[limit_rate]",
			}
		}
		for src, dst := range pairs {
			v := configString(cfg, src)
			if v == "" {
				continue
			}
			switch src {
			case "parameter14", "bt_flow":
				v = scaleInt(v, 1024) // G → MB
			case "parameter15", "bt_limit":
				v = scaleInt(v, 128) // MB/s → KB/s（×1024/8）
			}
			form.Set(dst, v)
		}
		return form
	}
	// 经典 bthosts：type == 1 是套餐开通，只带 plans_id。
	if configString(cfg, "type") == "1" {
		if pid := configString(cfg, "plans_id"); pid != "" {
			form.Set("plans_id", pid)
		}
		return form
	}
	// 自定义 / 弹性档：固定项 + 全量配置项（PHP 未给的键不覆盖上游默认）。
	form.Set("pack[ftp]", firstNonEmpty(configString(cfg, "ftp"), "1"))
	form.Set("pack[domain_audit]", firstNonEmpty(configString(cfg, "domain_audit"), "0"))
	form.Set("pack[session]", firstNonEmpty(configString(cfg, "session"), "0"))
	form.Set("pack[sql]", firstNonEmpty(configString(cfg, "sql"), "MySQL"))
	classic := map[string]string{
		"port": "pack[port]", "domain_num": "pack[domain_num]",
		"web_back_num": "pack[web_back_num]", "sql_back_num": "pack[sql_back_num]",
		"domainpools_id": "pack[domainpools_id]", "ippools_id": "pack[ippools_id]",
		"ip_num": "pack[ip_num]", "phpver": "pack[phpver]",
		"perserver": "pack[perserver]", "limit_rate": "pack[limit_rate]",
		"site_max": "pack[site_max]", "sql_max": "pack[sql_max]",
		"flow_max": "pack[flow_max]", "sub_bind": "pack[sub_bind]",
		"sort_id": "sort_id",
	}
	for src, dst := range classic {
		if v := configString(cfg, src); v != "" {
			form.Set(dst, v)
		}
	}
	return form
}

// Create 先建用户（user_create）再建主机（host_build），与参考实现 _CreateAccount 一致。
func (c *Client) Create(ctx context.Context, req provider.CreateRequest) (*provider.Instance, error) {
	name := siteName(req)
	password := randomPassword()
	userData, err := c.call(ctx, "/api/vhost/user_create", map[string]string{
		"username": name,
		"password": password,
	})
	if err != nil {
		return nil, err
	}
	uid := firstStringValue(userData, "id", "user_id")
	if uid == "" {
		return nil, errors.New("bthost 未返回用户 ID，无法继续开通")
	}
	form := buildCreateForm(configMap(req))
	payload := map[string]string{}
	for k, vals := range form {
		payload[k] = vals[0]
	}
	payload["user_id"] = uid
	// 到期时间：经典模块用 nextduedate；Provider 开通时没有交期，取 V10 的兜底值，
	// 续费时再由 Renew 同步真实到期时间。
	payload["endtime"] = "2099-12-31"
	siteData, err := c.call(ctx, "/api/vhost/host_build", payload)
	if err != nil {
		return nil, err
	}
	site, _ := siteData["site"].(map[string]any)
	siteID := firstStringValue(site, "id", "site_id")
	if siteID == "" {
		return nil, errors.New("bthost 未返回主机 ID，无法继续管理该主机")
	}
	host := ""
	if u, err := url.Parse(c.cfg.BaseURL); err == nil {
		host = u.Hostname()
	}
	return &provider.Instance{
		ID: siteID,
		Data: map[string]any{
			"site_id":     siteID,
			"username":    name,
			"password":    password,
			"panel":       c.cfg.BaseURL,
			"dedicatedip": host,
		},
	}, nil
}

// applySpeed 复刻 _Speed / _UnSpeed 的联动：并发或带宽为 0 时解除限速，否则开启限速。
func (c *Client) applySpeed(ctx context.Context, id, perserver, limitRate string) error {
	zero := func(s string) bool {
		s = strings.TrimSpace(s)
		if s == "" {
			return true
		}
		n, err := strconv.Atoi(s)
		return err == nil && n == 0
	}
	if zero(perserver) || zero(limitRate) {
		_, err := c.call(ctx, "/api/vhost/host_speedoff", map[string]string{"id": id})
		return err
	}
	_, err := c.call(ctx, "/api/vhost/host_speed", map[string]string{
		"id":         id,
		"perserver":  strings.TrimSpace(perserver),
		"limit_rate": strings.TrimSpace(limitRate),
	})
	return err
}

// ChangePackage 升降级：经典套餐切 host_update，其余改 host_edit；V10 自定义档不支持。
func (c *Client) ChangePackage(ctx context.Context, r provider.ChangePackageRequest) error {
	id := strings.TrimSpace(r.InstanceID)
	if id == "" {
		return errors.New("缺少 bthost 主机 ID")
	}
	cfg := r.SelectionsJSON
	if isV10(cfg) {
		if !isElastic(cfg) {
			return errors.New("bthostx 自定义配置（way=0）不支持升降级")
		}
		form := map[string]string{"id": id}
		setIf := func(dst, src string, mul int) {
			v := configString(cfg, src)
			if v == "" {
				return
			}
			if mul > 1 {
				v = scaleInt(v, mul)
			}
			form[dst] = v
		}
		setIf("site_max", "bt_site", 1)
		setIf("sql_max", "bt_sql", 1)
		setIf("domain_max", "bt_domain", 1)
		setIf("flow_max", "bt_flow", 1024)
		setIf("web_back_num", "bt_webback", 1)
		setIf("sql_back_num", "bt_sqlback", 1)
		setIf("sub_bind", "parameter2", 1)
		setIf("sort_id", "parameter6", 1)
		limit := scaleInt(configString(cfg, "bt_limit"), 128)
		if err := c.applySpeed(ctx, id, configString(cfg, "bt_perserver"), limit); err != nil {
			return err
		}
		_, err := c.call(ctx, "/api/vhost/host_edit", form)
		return err
	}
	if configString(cfg, "type") == "1" {
		if pid := configString(cfg, "plans_id"); pid != "" {
			_, err := c.call(ctx, "/api/vhost/host_update", map[string]string{"id": id, "plan_id": pid})
			return err
		}
	}
	form := map[string]string{"id": id}
	setIf := func(dst, src string) {
		if v := configString(cfg, src); v != "" {
			form[dst] = v
		}
	}
	setIf("site_max", "site_max")
	setIf("sql_max", "sql_max")
	setIf("domain_max", "domain_num")
	setIf("web_back_num", "web_back_num")
	setIf("flow_max", "flow_max")
	setIf("sql_back_num", "sql_back_num")
	setIf("sub_bind", "sub_bind")
	if err := c.applySpeed(ctx, id, configString(cfg, "perserver"), configString(cfg, "limit_rate")); err != nil {
		return err
	}
	_, err := c.call(ctx, "/api/vhost/host_edit", form)
	return err
}

func (c *Client) hostAction(ctx context.Context, ref, path string) error {
	id := strings.TrimSpace(ref)
	if id == "" {
		return errors.New("缺少 bthost 主机 ID")
	}
	_, err := c.call(ctx, path, map[string]string{"id": id})
	return err
}

// Suspend 暂停/锁定主机（host_locked）。
func (c *Client) Suspend(ctx context.Context, ref string) error {
	return c.hostAction(ctx, ref, "/api/vhost/host_locked")
}

// Unsuspend 启用主机（host_start）。
func (c *Client) Unsuspend(ctx context.Context, ref string) error {
	return c.hostAction(ctx, ref, "/api/vhost/host_start")
}

// Terminate 删除主机（host_recycle，进上游回收站）。
func (c *Client) Terminate(ctx context.Context, ref string) error {
	return c.hostAction(ctx, ref, "/api/vhost/host_recycle")
}

// Renew 复刻 _Renew：回收站恢复 + 启用 + 同步到期时间；前两步尽力而为，最后一步失败要报错。
func (c *Client) Renew(ctx context.Context, r provider.RenewRequest) error {
	id := strings.TrimSpace(r.InstanceID)
	if id == "" {
		return errors.New("缺少 bthost 主机 ID")
	}
	_, _ = c.call(ctx, "/api/vhost/host_recovery", map[string]string{"id": id})
	_, _ = c.call(ctx, "/api/vhost/host_start", map[string]string{"id": id})
	endtime := "2099-12-31"
	if !r.ExpiresAt.IsZero() {
		endtime = r.ExpiresAt.Format("2006-01-02")
	}
	_, err := c.call(ctx, "/api/vhost/host_endtime", map[string]string{"id": id, "endtime": endtime})
	return err
}

// firstStringValue 从返回体的 data 里按多个候选键取字符串。
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
			s := strings.TrimSpace(fmt.Sprint(t))
			if s != "" && s != "<nil>" {
				return s
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func truncateLog(body []byte) string {
	s := strings.TrimSpace(string(body))
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
