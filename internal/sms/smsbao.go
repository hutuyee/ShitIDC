// 短信宝（对应魔方 public/plugins/sms/smsbao）。
//
// GET http://api.smsbao.com/sms?u=<user>&p=<md5(pass)>&m=<phone>&c=<urlencoded content>
// 国际短信走 /wsms，号码去掉连字符。
// 响应是裸文本状态码："0" 成功，其余按插件的错误表给出人话。
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

// Smsbao 实现短信宝通道。
type Smsbao struct {
	// APIBase 可覆盖，便于测试指向本地假服务。
	APIBase string
}

// NewSmsbao 构造通道。
func NewSmsbao() *Smsbao {
	return &Smsbao{APIBase: "http://api.smsbao.com"}
}

// Name 返回通道标识。
func (s *Smsbao) Name() string { return "smsbao" }

func init() { Register(NewSmsbao()) }

// smsbaoStatus 与插件 statusStr 一致：按状态码给中文提示。
var smsbaoStatus = map[string]string{
	"30": "密码错误",
	"40": "账号不存在",
	"41": "余额不足",
	"42": "帐户已过期",
	"43": "IP地址限制",
	"50": "内容含有敏感词",
	"51": "手机号码不正确",
}

// Validate 检查必填配置。
func (s *Smsbao) Validate(cfg Config, secret Secret) error {
	if secret.Get("user") == "" || secret.Get("pass") == "" {
		return fmt.Errorf("短信宝需要平台账号 user 与密码 pass")
	}
	if cfg.Field("sign") == "" {
		return fmt.Errorf("短信宝需要短信签名 sign")
	}
	return nil
}

// Send 发送短信。content = 【签名】+ 渲染后的文本。
func (s *Smsbao) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := s.Validate(cfg, secret); err != nil {
		return err
	}
	action := "sms"
	phone := msg.Phone
	if !isMainlandPhone(phone) {
		action = "wsms"
		phone = strings.ReplaceAll(phone, "-", "")
	}
	content := wrapSMSign(cfg.Field("sign")) + renderSMSContent(cfg, msg, "content_template")
	sum := md5.Sum([]byte(secret.Get("pass")))

	target := fmt.Sprintf("%s/%s?%s", s.APIBase, action, url.Values{
		"u": {secret.Get("user")},
		"p": {hex.EncodeToString(sum[:])},
		"m": {phone},
		"c": {content},
	}.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("短信宝请求失败: %w", err)
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
	if text, ok := smsbaoStatus[code]; ok {
		return fmt.Errorf("短信宝发送失败: %s (code %s)", text, code)
	}
	return fmt.Errorf("短信宝发送失败: 未知的响应 %q", code)
}
