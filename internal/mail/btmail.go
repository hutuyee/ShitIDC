// 宝塔邮局（对应魔方 CBAP 插件包 public/plugins/mail/Btmail）。
//
// POST {宝塔面板}/mail_sys/send_mail_http.json 表单：
// mail_from / password / mail_to / subtype=html / subject / content。
// 成功判据：响应 JSON 的 status 必须是布尔 true（与参考实现一致，
// 字符串 "true" 不算——宝塔接口返回的就是布尔值）。
//
// 宝塔面板默认使用自签证书，参考实现直接关掉了证书校验；
// 这里做成配置项 insecure_tls（默认开启以对齐行为），要求安全可显式关闭。
package mail

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Btmail 实现宝塔邮局通道。
type Btmail struct {
	http *http.Client
}

// NewBtmail 构造通道。
func NewBtmail() *Btmail { return &Btmail{} }

// Name 返回通道标识。
func (b *Btmail) Name() string { return "btmail" }

func init() { Register(NewBtmail()) }

// Validate 检查必填配置。
func (b *Btmail) Validate(cfg Config, secret Secret) error {
	host := cfg.Field("host")
	if host == "" {
		return fmt.Errorf("宝塔邮局需要面板地址 host")
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		return fmt.Errorf("宝塔邮局面板地址需要以 http:// 或 https:// 开头")
	}
	if cfg.Field("from_address") == "" {
		return fmt.Errorf("宝塔邮局需要发件地址 from_address")
	}
	if secret.Get("password") == "" {
		return fmt.Errorf("宝塔邮局需要邮箱密码 password")
	}
	return nil
}

// Send 调用宝塔邮局 HTTP 接口。
func (b *Btmail) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := b.Validate(cfg, secret); err != nil {
		return err
	}
	form := url.Values{}
	form.Set("mail_from", cfg.Field("from_address"))
	form.Set("password", secret.Get("password"))
	form.Set("mail_to", msg.To)
	form.Set("subtype", "html")
	form.Set("subject", msg.Subject)
	form.Set("content", msg.HTML)

	endpoint := strings.TrimRight(cfg.Field("host"), "/") + "/mail_sys/send_mail_http.json"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 15 * time.Second}
	if cfg.Field("insecure_tls") != "false" {
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("宝塔邮局请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var out struct {
		Status *bool  `json:"status"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("宝塔邮局返回无法解析(HTTP %d)：%s", resp.StatusCode, truncateBody(string(body)))
	}
	if out.Status == nil || !*out.Status {
		return fmt.Errorf("宝塔邮局发送失败：%s", firstNonEmpty(out.Msg, "未知错误"))
	}
	return nil
}
