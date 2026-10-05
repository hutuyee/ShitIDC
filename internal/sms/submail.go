// 赛邮 Submail（对应魔方 public/plugins/sms/submail）。
//
// 与参考插件一致走「普通文本发送」而不是模板发送：
//
//	国内  POST https://api.mysubmail.com/message/send.json
//	国际  POST https://api.mysubmail.com/internationalsms/send.json
//
// 表单字段：appid / appkey / signature(=appkey) / timestamp / to / content。
// content 以【签名】开头（插件会把签名统一成中文括号包裹）。
// 成功判据：status == "success"。
//
// 国际短信按号码自动分流：非中国大陆号码走国际端点；国际凭据未配置时明确报错，
// 而不是悄悄用国内通道发国际号。
package sms

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

// Submail 实现赛邮通道。
type Submail struct {
	// APIBase 可覆盖，便于测试指向本地假服务。
	APIBase string
	// Now 允许测试注入。
	Now func() time.Time
}

// NewSubmail 构造通道。
func NewSubmail() *Submail {
	return &Submail{APIBase: "https://api.mysubmail.com/", Now: time.Now}
}

// Name 返回通道标识。
func (s *Submail) Name() string { return "submail" }

func init() { Register(NewSubmail()) }

// Validate 检查必填配置。国际凭据是可选项。
func (s *Submail) Validate(cfg Config, secret Secret) error {
	if cfg.Field("app_id") == "" || secret.Get("app_key") == "" {
		return fmt.Errorf("赛邮短信需要 app_id 与 app_key")
	}
	if cfg.Field("app_sign") == "" {
		return fmt.Errorf("赛邮短信需要短信签名 app_sign")
	}
	return nil
}

// Send 发送短信。content = 【签名】+ 渲染后的文本。
func (s *Submail) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := s.Validate(cfg, secret); err != nil {
		return err
	}
	text := renderSMSContent(cfg, msg, "content_template")
	international := !isMainlandPhone(msg.Phone)

	apiID, apiKey, sign := cfg.Field("app_id"), secret.Get("app_key"), cfg.Field("app_sign")
	endpoint := s.APIBase + "message/send.json"
	if international {
		endpoint = s.APIBase + "internationalsms/send.json"
		apiID = cfg.Field("international_app_id")
		apiKey = secret.Get("international_app_key")
		sign = cfg.Field("international_app_sign")
		if apiID == "" || apiKey == "" || sign == "" {
			return fmt.Errorf("国际短信需要配置 international_app_id / international_app_key / international_app_sign")
		}
	}

	form := url.Values{}
	form.Set("appid", apiID)
	form.Set("appkey", apiKey)
	form.Set("signature", apiKey)
	form.Set("timestamp", fmt.Sprintf("%d", s.Now().Unix()))
	form.Set("to", msg.Phone)
	form.Set("content", wrapSMSign(sign)+text)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("赛邮短信请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var out struct {
		Status string `json:"status"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return fmt.Errorf("赛邮短信返回无法解析(HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if out.Status != "success" {
		return fmt.Errorf("赛邮短信发送失败: %s", firstNonEmptyStr(out.Msg, "未知错误"))
	}
	return nil
}

// wrapSMSign 把签名统一成中文括号包裹（与插件 templateSign 一致）。
func wrapSMSign(sign string) string {
	sign = strings.TrimSpace(sign)
	sign = strings.ReplaceAll(sign, "【", "")
	sign = strings.ReplaceAll(sign, "】", "")
	return "【" + sign + "】"
}

// renderSMSContent 渲染文本模板：{code} 换验证码，{ttl} 换有效分钟数。
// 未配置模板时给出一个默认文案——宁可文案朴素也不能发出没有验证码的短信。
func renderSMSContent(cfg Config, msg Message, key string) string {
	tpl := cfg.Field(key)
	if tpl == "" {
		tpl = "您的验证码是{code}，{ttl}分钟内有效，请勿泄露给他人。"
	}
	tpl = strings.ReplaceAll(tpl, "{code}", msg.Code)
	tpl = strings.ReplaceAll(tpl, "{ttl}", fmt.Sprintf("%d", maxInt(msg.TTLMinutes, 1)))
	return tpl
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
