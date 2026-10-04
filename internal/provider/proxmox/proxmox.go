// Package proxmox implements the resource Provider interface (§11) for
// Proxmox VE using API token authentication.
//
// Provider config (stored in providers.config JSONB):
//   - node:         target PVE node name
//   - api_token_id: "user@realm!tokenid"
//   - vm_type:      "lxc" or "qemu"
//   - template_vmid / ostemplate: source template
//   - cores / memory / storage / bridge / disk_gb: instance defaults
//   - vmid_range:   "200-299" (optional allocation guard)
//
// The API token secret (providers.secret_encrypted) is the token value UUID.
package proxmox

import (
	"bytes"
	"context"
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
	BaseURL      string         `json:"base_url"`
	Node         string         `json:"node"`
	APITokenID   string         `json:"api_token_id"`
	APIToken     string         `json:"-"`
	VMType       string         `json:"vm_type"` // lxc | qemu
	OSTemplate   string         `json:"ostemplate"`
	TemplateVMID int            `json:"template_vmid"`
	Cores        int            `json:"cores"`
	MemoryMB     int            `json:"memory"`
	Storage      string         `json:"storage"`
	DiskGB       int            `json:"disk_gb"`
	Bridge       string         `json:"bridge"`
	Password     string         `json:"password"`
	AllowPrivate bool           `json:"allow_private"`
	Extra        map[string]any `json:"extra"`
}

type Client struct {
	cfg    Config
	client *http.Client
}

func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.Node) == "" {
		return nil, errors.New("proxmox 需要 base_url 与 node 配置")
	}
	if strings.TrimSpace(cfg.APITokenID) == "" || strings.TrimSpace(cfg.APIToken) == "" {
		return nil, errors.New("proxmox 需要 api_token_id 与 API Token")
	}
	cfg.VMType = strings.ToLower(strings.TrimSpace(cfg.VMType))
	if cfg.VMType == "" {
		cfg.VMType = "lxc"
	}
	if cfg.VMType != "lxc" && cfg.VMType != "qemu" {
		return nil, errors.New("proxmox vm_type 仅支持 lxc 或 qemu")
	}
	if !strings.HasPrefix(cfg.BaseURL, "https://") && !cfg.AllowPrivate {
		return nil, errors.New("proxmox base_url 必须为 HTTPS（或显式允许内网）")
	}
	client := security.SafeHTTPClient(cfg.AllowPrivate, 60*time.Second)
	return &Client{cfg: cfg, client: client}, nil
}

func (c *Client) do(ctx context.Context, method, path string, form url.Values) (json.RawMessage, error) {
	target := strings.TrimRight(c.cfg.BaseURL, "/") + path
	var body io.Reader
	if form != nil {
		body = bytes.NewBufferString(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+c.cfg.APITokenID+"="+c.cfg.APIToken)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("proxmox %s: HTTP %d: %s", path, resp.StatusCode, truncate(raw, 200))
	}
	return json.RawMessage(raw), nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		return string(b[:n])
	}
	return string(b)
}

func (c *Client) TestConnection(ctx context.Context) error {
	if _, err := c.do(ctx, http.MethodGet, "/api2/json/version", nil); err != nil {
		return err
	}
	return nil
}

// nextVMID reserves the next free VM id from the node.
func (c *Client) nextVMID(ctx context.Context) (int, error) {
	raw, err := c.do(ctx, http.MethodGet, "/api2/json/cluster/nextid", nil)
	if err != nil {
		return 0, err
	}
	var wrap struct {
		Data any `json:"data"`
	}
	_ = json.Unmarshal(raw, &wrap)
	switch v := wrap.Data.(type) {
	case string:
		return strconv.Atoi(v)
	case float64:
		return int(v), nil
	}
	return 0, errors.New("proxmox nextid 无效")
}

func (c *Client) Create(ctx context.Context, req provider.CreateRequest) (*provider.Instance, error) {
	vmid, err := c.nextVMID(ctx)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	var taskPath string
	if c.cfg.VMType == "lxc" {
		if c.cfg.OSTemplate == "" {
			return nil, errors.New("proxmox lxc 需要 ostemplate 配置")
		}
		form.Set("ostemplate", c.cfg.OSTemplate)
		form.Set("vmid", strconv.Itoa(vmid))
		form.Set("hostname", fmt.Sprintf("idc-%s", req.RequestID[:8]))
		if c.cfg.Password != "" {
			form.Set("password", c.cfg.Password)
		}
		form.Set("cores", intOr(c.cfg.Cores, 1))
		form.Set("memory", intOr(c.cfg.MemoryMB, 512))
		rootfs := fmt.Sprintf("%s:%d", orStr(c.cfg.Storage, "local-lvm"), orInt(c.cfg.DiskGB, 8))
		form.Set("rootfs", rootfs)
		if c.cfg.Bridge != "" {
			net := fmt.Sprintf("name=eth0,bridge=%s,ip=dhcp", c.cfg.Bridge)
			form.Set("net0", net)
		}
		taskPath = fmt.Sprintf("/api2/json/nodes/%s/lxc", c.cfg.Node)
	} else {
		if c.cfg.TemplateVMID <= 0 {
			return nil, errors.New("proxmox qemu 需要 template_vmid 配置")
		}
		form.Set("vmid", strconv.Itoa(vmid))
		form.Set("name", fmt.Sprintf("idc-%s", req.RequestID[:8]))
		form.Set("clone", strconv.Itoa(c.cfg.TemplateVMID))
		form.Set("full", "1")
		if c.cfg.Storage != "" {
			form.Set("storage", c.cfg.Storage)
		}
		taskPath = fmt.Sprintf("/api2/json/nodes/%s/qemu", c.cfg.Node)
	}
	for k, v := range c.cfg.Extra {
		if s, ok := v.(string); ok && s != "" {
			form.Set(k, s)
		}
	}
	if _, err := c.do(ctx, http.MethodPost, taskPath, form); err != nil {
		return nil, err
	}
	// The instance is identified by VMID; creation runs as a PVE task.
	return &provider.Instance{ID: fmt.Sprintf("pve:%d:%s", vmid, c.cfg.Node), Data: map[string]any{"vmid": vmid, "node": c.cfg.Node, "vm_type": c.cfg.VMType, "task": "create"}}, nil
}

func (c *Client) parseRef(ref string) (int, error) {
	// ref format: pve:<vmid>:<node>
	parts := strings.Split(ref, ":")
	if len(parts) != 3 || parts[0] != "pve" {
		return 0, fmt.Errorf("invalid proxmox ref %q", ref)
	}
	vmid, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, fmt.Errorf("invalid proxmox vmid in %q", ref)
	}
	return vmid, nil
}

func (c *Client) vmAction(ctx context.Context, ref, action string) error {
	vmid, err := c.parseRef(ref)
	if err != nil {
		return err
	}
	kind := c.cfg.VMType
	endpoint := fmt.Sprintf("/api2/json/nodes/%s/%s/%d/status/%s", c.cfg.Node, kind, vmid, action)
	if _, err := c.do(ctx, http.MethodPost, endpoint, url.Values{}); err != nil {
		// qemu destroy uses DELETE
		if action == "destroy" {
			del := fmt.Sprintf("/api2/json/nodes/%s/%s/%d", c.cfg.Node, kind, vmid)
			if _, derr := c.do(ctx, http.MethodDelete, del, nil); derr == nil {
				return nil
			}
		}
		return err
	}
	return nil
}

func (c *Client) Suspend(ctx context.Context, ref string) error {
	action := "suspend"
	if c.cfg.VMType == "lxc" {
		action = "freeze"
	}
	return c.vmAction(ctx, ref, action)
}

func (c *Client) Unsuspend(ctx context.Context, ref string) error {
	action := "resume"
	if c.cfg.VMType == "lxc" {
		action = "unfreeze"
	}
	return c.vmAction(ctx, ref, action)
}

func (c *Client) Terminate(ctx context.Context, ref string) error {
	return c.vmAction(ctx, ref, "destroy")
}

func (c *Client) Renew(context.Context, string) error { return nil }

// ChangePackage resizes an existing guest (cores / memory) and applies the new
// limits through the PVE config endpoint. Only the resources the caller sent
// are touched; when nothing actionable arrives the provider reports that the
// upstream cannot change the package, so callers fall back to local bookkeeping.
func (c *Client) ChangePackage(ctx context.Context, req provider.ChangePackageRequest) error {
	vmid, err := c.parseRef(req.InstanceID)
	if err != nil {
		return err
	}
	kind := "lxc"
	if c.cfg.VMType == "qemu" {
		kind = "qemu"
	}
	form := url.Values{}
	if v, ok := numericSelection(req.SelectionsJSON, "cores"); ok {
		form.Set("cores", strconv.FormatInt(v, 10))
	}
	if v, ok := numericSelection(req.SelectionsJSON, "memory"); ok {
		form.Set("memory", strconv.FormatInt(v, 10))
	}
	if len(form) == 0 {
		return provider.ErrChangePackageUnsupported
	}
	_, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/api2/json/nodes/%s/%s/%d/config", url.PathEscape(c.cfg.Node), kind, vmid), form)
	return err
}

// numericSelection reads a positive integer out of the selections payload,
// accepting JSON numbers and numeric strings from config options.
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

func intOr(v, def int) string {
	if v <= 0 {
		return strconv.Itoa(def)
	}
	return strconv.Itoa(v)
}

func orInt(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

func orStr(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}
