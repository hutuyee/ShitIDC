// 赛邮邮件（Submail，对应魔方 public/plugins/mail/subemail）。
//
// POST https://api.mysubmail.com/mail/send 表单提交：
// appid / from / from_name / signature(=appkey) / to / subject / html。
// 成功判据：status == "success"；失败时 code 可查抄插件里的错误码表。
package mail

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Subemail 实现赛邮邮件通道。
type Subemail struct {
	// APIBase 可覆盖，便于测试指向本地假服务。
	APIBase string
}

// NewSubemail 构造通道。
func NewSubemail() *Subemail { return &Subemail{APIBase: "https://api.mysubmail.com/"} }

// Name 返回通道标识。
func (s *Subemail) Name() string { return "subemail" }

func init() { Register(NewSubemail()) }

// subemailCodes 抄自参考插件的错误码表，便于把 API 错误翻成人话。
var subemailCodes = map[string]string{
	"101": "不正确的 APP ID",
	"102": "此应用已被禁用，请至 submail 应用集成页面开启",
	"103": "未启用的开发者，开发者身份未验证",
	"104": "此开发者未通过验证或资料发生更改",
	"105": "此账户已过期",
	"106": "此账户已被禁用",
	"108": "signature 参数无效",
	"109": "appkey 无效",
	"111": "空的 signature 参数",
	"205": "错误的发件人地址",
	"206": "错误的发件域，域名未通过 SUBMAIL 验证",
	"301": "邮件标题不能为空",
	"302": "邮件标题不能超过 100 个字符",
	"303": "没有填写邮件内容",
	"901": "今日发送配额已用尽",
	"902": "邮件发送许可已用尽或余额不足",
}

// Validate 检查必填配置。
func (s *Subemail) Validate(cfg Config, secret Secret) error {
	if cfg.Field("app_id") == "" {
		return fmt.Errorf("赛邮邮件需要应用 ID app_id")
	}
	if cfg.Field("from_address") == "" {
		return fmt.Errorf("赛邮邮件需要发件人地址 from_address")
	}
	if secret.Get("app_key") == "" {
		return fmt.Errorf("赛邮邮件需要应用密钥 app_key")
	}
	return nil
}

// Send 调用 mail/send 接口。
func (s *Subemail) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := s.Validate(cfg, secret); err != nil {
		return err
	}
	form := url.Values{}
	form.Set("appid", cfg.Field("app_id"))
	form.Set("from", cfg.Field("from_address"))
	if fromName := cfg.Field("from_name"); fromName != "" {
		form.Set("from_name", fromName)
	}
	form.Set("signature", secret.Get("app_key"))
	form.Set("to", msg.To)
	form.Set("subject", msg.Subject)
	form.Set("html", msg.HTML)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.APIBase+"mail/send", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("赛邮邮件请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var out struct {
		Status string `json:"status"`
		Code   string `json:"code"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("赛邮邮件返回无法解析(HTTP %d)：%s", resp.StatusCode, truncateBody(string(body)))
	}
	if out.Status != "success" {
		detail := firstNonEmpty(subemailCodes[out.Code], out.Msg, "未知错误")
		return fmt.Errorf("赛邮邮件发送失败[%s]：%s", out.Code, detail)
	}
	return nil
}
