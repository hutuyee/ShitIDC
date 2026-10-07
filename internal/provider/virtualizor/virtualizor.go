// Package virtualizor implements the resource Provider interface (§11) for
// Virtualizor admin API (key + HMAC-SHA256 timestamp signature).
//
// Provider config (providers.config JSONB):
//   - serverid:     virtualizor server id
//   - plan / osid / ips / space_gb / ram_mb / cores: VPS defaults
//   - allow_private: permit intranet panel addresses
//   - allow_insecure_tls: 显式允许自签证书（默认 false，开启后失去中间人防护）
//
// providers.secret_encrypted holds the API key + password JSON:
//
//	{"api_key":"...","api_pass":"..."}
package virtualizor

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hutuyee/ShitIDC/internal/provider"
	"github.com/hutuyee/ShitIDC/internal/security"
)

type Config struct {
	BaseURL      string `json:"base_url"`
	ServerID     string `json:"serverid"`
	APIKey       string `json:"-"`
	APIPass      string `json:"-"`
	Plan         string `json:"plan"`
	OSID         string `json:"osid"`
	SpaceGB      int    `json:"space_gb"`
	RAMMB        int    `json:"ram_mb"`
	Cores        int    `json:"cores"`
	BandwidthGB  int    `json:"bandwidth_gb"`
	AllowPrivate bool   `json:"allow_private"`
	// AllowInsecureTLS 仅在显式配置时跳过证书校验（Virtualizor 常用自签证书）。
	AllowInsecureTLS bool `json:"allow_insecure_tls"`
}

type Client struct {
	cfg Config
	hc  *http.Client
}

func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("virtualizor 需要 base_url 配置")
	}
	if cfg.APIKey == "" || cfg.APIPass == "" {
		return nil, errors.New("virtualizor 需要 API Key 与 API Pass")
	}
	hc := security.SafeHTTPClient(cfg.AllowPrivate, 60*time.Second)
	if cfg.AllowInsecureTLS {
		if t, ok := hc.Transport.(*http.Transport); ok {
			// 仅显式开启时跳过证书校验；自签面板请改用受信证书。
			t.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
	}
	return &Client{cfg: cfg, hc: hc}, nil
}

// signedQuery builds the Virtualizor admin API signature:
// SHA256(api_key|timestamp|api_pass) appended as parameters (act=... protocol).
func (c *Client) signedQuery(act string, extra url.Values) url.Values {
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	sum := sha256.Sum256([]byte(c.cfg.APIKey + "|" + timestamp + "|" + c.cfg.APIPass))
	q := url.Values{}
	q.Set("act", act)
	q.Set("api", "json")
	q.Set("apikey", c.cfg.APIKey)
	q.Set("timestamp", timestamp)
	q.Set("signature", hex.EncodeToString(sum[:]))
	for k, vs := range extra {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return q
}

func (c *Client) get(ctx context.Context, act string, extra url.Values) (map[string]any, error) {
	target := strings.TrimRight(c.cfg.BaseURL, "/") + "/index.php?" + c.signedQuery(act, extra).Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("virtualizor %s: HTTP %d: %s", act, resp.StatusCode, truncate(body, 200))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("virtualizor %s: 响应解析失败: %s", act, truncate(body, 200))
	}
	return out, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

func (c *Client) TestConnection(ctx context.Context) error {
	out, err := c.get(ctx, "vs", nil)
	if err != nil {
		return err
	}
	if msg, ok := out["error"].(map[string]any); ok {
		if m, _ := msg["msg"].(string); m != "" {
			return fmt.Errorf("virtualizor: %s", m)
		}
	}
	return nil
}

// Create provisions a VPS via act=addvs.
func (c *Client) Create(ctx context.Context, req provider.CreateRequest) (*provider.Instance, error) {
	extra := url.Values{}
	extra.Set("addvps", "1")
	serverID := c.cfg.ServerID
	if serverID == "" {
		serverID = "0"
	}
	extra.Set("vps[server_id]", serverID)
	if c.cfg.Plan != "" {
		extra.Set("vps[plid]", c.cfg.Plan)
	}
	if c.cfg.OSID != "" {
		extra.Set("vps[osid]", c.cfg.OSID)
	}
	extra.Set("vps[space]", strconv.Itoa(orInt(c.cfg.SpaceGB, 10)))
	extra.Set("vps[ram]", strconv.Itoa(orInt(c.cfg.RAMMB, 512)))
	extra.Set("vps[cores]", strconv.Itoa(orInt(c.cfg.Cores, 1)))
	if c.cfg.BandwidthGB > 0 {
		extra.Set("vps[bw]", strconv.Itoa(orInt(c.cfg.BandwidthGB, 0)))
	}
	extra.Set("vps[hostname]", "idc-"+sanitizeHostname(req.RequestID))
	out, err := c.get(ctx, "addvs", extra)
	if err != nil {
		return nil, err
	}
	if msg, ok := out["error"].(map[string]any); ok {
		if m, _ := msg["msg"].(string); m != "" && m != "None" {
			return nil, fmt.Errorf("virtualizor create: %s", m)
		}
	}
	vpsID := extractVPSID(out)
	if vpsID == "" {
		return nil, errors.New("virtualizor create: 响应中缺少 vps id")
	}
	return &provider.Instance{ID: "vz:" + vpsID, Data: map[string]any{"vpsid": vpsID, "response": out}}, nil
}

func extractVPSID(out map[string]any) string {
	if info, ok := out["info"].(map[string]any); ok {
		if id, ok := info["vpsid"].(string); ok && id != "" {
			return id
		}
		if f, ok := info["vpsid"].(float64); ok {
			return strconv.Itoa(int(f))
		}
	}
	if newvs, ok := out["newvs"].(map[string]any); ok && len(newvs) > 0 {
		for id := range newvs {
			return id
		}
	}
	return ""
}

func (c *Client) vpsAction(ctx context.Context, act string, extra url.Values) (map[string]any, error) {
	out, err := c.get(ctx, act, extra)
	if err != nil {
		return nil, err
	}
	if msg, ok := out["error"].(map[string]any); ok {
		if m, _ := msg["msg"].(string); m != "" && m != "None" {
			return nil, fmt.Errorf("virtualizor %s: %s", act, m)
		}
	}
	return out, nil
}

// Suspend flags the VPS as suspended (act=vpsmanage&suspend=1).
func (c *Client) Suspend(ctx context.Context, ref string) error {
	extra := url.Values{"vpsid": {strings.TrimPrefix(ref, "vz:")}, "suspend": {"1"}}
	_, err := c.vpsAction(ctx, "vpsmanage", extra)
	return err
}

// Unsuspend clears the suspended flag (act=vpsmanage&unsuspend=1).
func (c *Client) Unsuspend(ctx context.Context, ref string) error {
	extra := url.Values{"vpsid": {strings.TrimPrefix(ref, "vz:")}, "unsuspend": {"1"}}
	_, err := c.vpsAction(ctx, "vpsmanage", extra)
	return err
}

// Terminate uses act=vpsterminate which permanently destroys the VPS.
func (c *Client) Terminate(ctx context.Context, ref string) error {
	extra := url.Values{"vpsid": {strings.TrimPrefix(ref, "vz:")}, "deletevps": {"1"}}
	_, err := c.vpsAction(ctx, "vpsterminate", extra)
	return err
}

func (c *Client) Renew(context.Context, provider.RenewRequest) error { return nil }

// ChangePackage calls Virtualizor's editvs to move an existing VPS to another
// plan and/or override its resources. Virtualizor takes a plan id (plid) plus
// resource values, so everything comes from the config-option selections.
func (c *Client) ChangePackage(ctx context.Context, req provider.ChangePackageRequest) error {
	extra := url.Values{}
	extra.Set("vpsid", req.InstanceID)
	if plan, ok := stringSelection(req.SelectionsJSON, "plan"); ok {
		extra.Set("plid", plan)
	}
	if v, ok := numericSelection(req.SelectionsJSON, "space_gb"); ok {
		extra.Set("space", strconv.FormatInt(v, 10))
	}
	if v, ok := numericSelection(req.SelectionsJSON, "ram_mb"); ok {
		extra.Set("ram", strconv.FormatInt(v, 10))
	}
	if v, ok := numericSelection(req.SelectionsJSON, "cores"); ok {
		extra.Set("cores", strconv.FormatInt(v, 10))
	}
	if v, ok := numericSelection(req.SelectionsJSON, "bandwidth_gb"); ok {
		extra.Set("bandwidth", strconv.FormatInt(v, 10))
	}
	// 只带 vpsid 说明没有任何可下发的变更。
	if extra.Get("plid") == "" && len(extra) == 1 {
		return provider.ErrChangePackageUnsupported
	}
	_, err := c.vpsAction(ctx, "editvs", extra)
	return err
}

func numericSelection(sel map[string]any, key string) (int64, bool) {
	if sel == nil {
		return 0, false
	}
	v, ok := sel[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), n > 0
	case int64:
		return n, n > 0
	case int:
		return int64(n), n > 0
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return parsed, err == nil && parsed > 0
	default:
		return 0, false
	}
}

func stringSelection(sel map[string]any, key string) (string, bool) {
	if sel == nil {
		return "", false
	}
	v, ok := sel[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	s = strings.TrimSpace(s)
	return s, ok && s != ""
}

func sanitizeHostname(id string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-':
			return r
		}
		return -1
	}, id)
	if len(out) > 12 {
		out = out[:12]
	}
	if out == "" {
		return "vps"
	}
	return out
}

func orInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}
