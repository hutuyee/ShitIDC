// 布丁云短信 v10（对应魔方 CBAP 插件包 public/plugins/sms/Puddingv10sms）。
//
// POST https://sms.idcbdy.cn/sendApi（表单）：
// channel / username / key=md5(secretKey) / phone / content=【签名】+文案。
// 成功判据：响应 code == 1——PHP 是松散比较，数字 1 与字符串 "1" 都算，
// 这里统一成字符串比对，避免把 "1" 判成失败。
package sms

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Puddingv10sms 实现布丁云短信 v10 通道。
type Puddingv10sms struct {
	http *http.Client
	// Endpoint 可覆盖，便于测试指向本地假服务。
	Endpoint string
}

// NewPuddingv10sms 构造通道。
func NewPuddingv10sms() *Puddingv10sms {
	return &Puddingv10sms{
		http:     &http.Client{Timeout: 15 * time.Second},
		Endpoint: "https://sms.idcbdy.cn/sendApi",
	}
}

// Name 返回通道标识。
func (p *Puddingv10sms) Name() string { return "puddingv10sms" }

func init() { Register(NewPuddingv10sms()) }

// Validate 检查必填配置。
func (p *Puddingv10sms) Validate(cfg Config, secret Secret) error {
	if cfg.Field("username") == "" {
		return fmt.Errorf("布丁云短信需要平台用户名 username")
	}
	if cfg.Field("channel") == "" {
		return fmt.Errorf("布丁云短信需要发信通道 channel")
	}
	if cfg.Field("sign") == "" {
		return fmt.Errorf("布丁云短信需要短信签名 sign")
	}
	if secret.Get("key") == "" {
		return fmt.Errorf("布丁云短信需要用户 Secret Key")
	}
	return nil
}

// Send 调用 sendApi 接口。
func (p *Puddingv10sms) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := p.Validate(cfg, secret); err != nil {
		return err
	}
	sum := md5.Sum([]byte(secret.Get("key")))
	form := url.Values{
		"channel":  {cfg.Field("channel")},
		"username": {cfg.Field("username")},
		"key":      {hex.EncodeToString(sum[:])},
		"phone":    {strings.TrimSpace(msg.Phone)},
		"content":  {wrapSMSign(cfg.Field("sign")) + renderSMSContent(cfg, msg, "content_template")},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("布丁云短信请求失败: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var out struct {
		Code any    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("布丁云短信返回无法解析(HTTP %d)：%s", resp.StatusCode, truncateSMS(string(raw)))
	}
	if code := anyToString(out.Code); code != "1" {
		return fmt.Errorf("布丁云短信发送失败[%s]：%s", code, firstNonEmptyStr(out.Msg, "未知错误"))
	}
	return nil
}
