// 通用短信宝式接口（对应魔方 CBAP 插件包 public/plugins/sms/Tysms）。
//
// GET {url}?u={user}&p=md5(pass)&m={phone}&c=urlencode(【签名】+文案)
// 响应是裸文本状态码："0" 成功，其余按插件的 statusStr 翻成人话；
// 其中两个 uint64 溢出码是原实现的历史遗留，但真实平台会返回，照收。
package sms

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Tysms 实现通用短信宝式接口通道。
type Tysms struct{}

// NewTysms 构造通道。
func NewTysms() *Tysms { return &Tysms{} }

// Name 返回通道标识。
func (t *Tysms) Name() string { return "tysms" }

func init() { Register(NewTysms()) }

// tysmsStatus 与插件 statusStr 一致。
var tysmsStatus = map[string]string{
	"18446744073709551615": "参数不全",
	"18446744073709551614": "服务器空间不支持,请确认支持curl或者fsocket，联系您的空间商解决或者更换空间！",
	"30":                   "密码错误",
	"40":                   "账号不存在",
	"41":                   "余额不足",
	"42":                   "帐户已过期",
	"43":                   "IP地址限制",
	"50":                   "内容含有敏感词",
	"51":                   "手机号码不正确",
}

// Validate 检查必填配置。
func (t *Tysms) Validate(cfg Config, secret Secret) error {
	if cfg.Field("url") == "" {
		return fmt.Errorf("通用短信宝式接口需要接口地址 url")
	}
	if cfg.Field("sign") == "" {
		return fmt.Errorf("通用短信宝式接口需要短信签名 sign")
	}
	if secret.Get("user") == "" || secret.Get("pass") == "" {
		return fmt.Errorf("通用短信宝式接口需要平台账号 user 与密码 pass")
	}
	return nil
}

// Send 发送短信。content = 【签名】+ 渲染后的文本。
func (t *Tysms) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := t.Validate(cfg, secret); err != nil {
		return err
	}
	content := wrapSMSign(cfg.Field("sign")) + renderSMSContent(cfg, msg, "content_template")
	sum := md5.Sum([]byte(secret.Get("pass")))
	target := cfg.Field("url") + "?" + url.Values{
		"u": {secret.Get("user")},
		"p": {hex.EncodeToString(sum[:])},
		"m": {strings.TrimSpace(msg.Phone)},
		"c": {content},
	}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("通用短信宝式接口请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	code := strings.TrimSpace(string(body))
	if code == "0" {
		return nil
	}
	if text, ok := tysmsStatus[code]; ok {
		return fmt.Errorf("通用短信宝式接口发送失败: %s (code %s)", text, code)
	}
	return fmt.Errorf("通用短信宝式接口发送失败: 未知的响应 %q", code)
}
