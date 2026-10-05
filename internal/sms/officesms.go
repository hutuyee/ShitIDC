// 第二办公室短信（对应魔方 CBAP 插件包 public/plugins/sms/Officesms）。
//
// POST http://open.2office.cn/Accounts/{account}/Sms/SendSms?sign=md5(account+authCode+timestamp)
// 请求头 Authorization: 大写(base64(account:timestamp))，JSON 请求体：
// appId / mobile / content / channel / smsid / sendType=1 / timestamp。
// 成功判据：响应 code == "0000000"。
//
// 参考实现有两处笔误，这里按正确语义处理并在测试里钉死：
//   - appId 取的是不存在的 config["appid"]（配置里叫 account）；
//   - processSendResult 的 status 赋值是一句无效表达式，导致它永远进 error 分支。
package sms

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Officesms 实现第二办公室短信通道。
type Officesms struct {
	http *http.Client
	// Endpoint 可覆盖，便于测试指向本地假服务。
	Endpoint string
	// Now 允许测试注入。
	Now func() time.Time
}

// NewOfficesms 构造通道。
func NewOfficesms() *Officesms {
	return &Officesms{
		http:     &http.Client{Timeout: 15 * time.Second},
		Endpoint: "http://open.2office.cn",
		Now:      time.Now,
	}
}

// Name 返回通道标识。
func (o *Officesms) Name() string { return "officesms" }

func init() { Register(NewOfficesms()) }

// Validate 检查必填配置。
func (o *Officesms) Validate(cfg Config, secret Secret) error {
	if cfg.Field("account") == "" {
		return fmt.Errorf("第二办公室短信需要账号 account")
	}
	if cfg.Field("channel") == "" {
		return fmt.Errorf("第二办公室短信需要通道编码 channel")
	}
	if cfg.Field("signature") == "" {
		return fmt.Errorf("第二办公室短信需要短信签名 signature")
	}
	if secret.Get("auth_code") == "" {
		return fmt.Errorf("第二办公室短信需要授权码 auth_code")
	}
	return nil
}

// Send 调用 SendSms 接口。
func (o *Officesms) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := o.Validate(cfg, secret); err != nil {
		return err
	}
	account := cfg.Field("account")
	authCode := secret.Get("auth_code")
	ts := o.Now().Unix()
	sum := md5.Sum([]byte(account + authCode + strconv.FormatInt(ts, 10)))
	target := o.Endpoint + "/Accounts/" + account + "/Sms/SendSms?sign=" + hex.EncodeToString(sum[:])

	body, err := json.Marshal(map[string]any{
		"appId":     account,
		"mobile":    msg.Phone,
		"content":   wrapSMSign(cfg.Field("signature")) + renderSMSContent(cfg, msg, "content_template"),
		"channel":   cfg.Field("channel"),
		"smsid":     strconv.FormatInt(o.Now().UnixNano(), 10),
		"sendType":  "1",
		"timestamp": ts,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json;charset=UTF-8")
	req.Header.Set("Authorization", strings.ToUpper(base64.StdEncoding.EncodeToString([]byte(account+":"+strconv.FormatInt(ts, 10)))))
	resp, err := o.http.Do(req)
	if err != nil {
		return fmt.Errorf("第二办公室短信请求失败: %w", err)
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
		return fmt.Errorf("第二办公室短信返回无法解析(HTTP %d)：%s", resp.StatusCode, truncateSMS(string(raw)))
	}
	if code := anyToString(out.Code); code != "0000000" {
		return fmt.Errorf("第二办公室短信发送失败[%s]：%s", code, firstNonEmptyStr(out.Msg, "未知错误"))
	}
	return nil
}

// anyToString 把 JSON 里的数字/字符串统一成字符串。
func anyToString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case float64:
		return strconv.FormatInt(int64(t), 10)
	default:
		return fmt.Sprint(t)
	}
}
