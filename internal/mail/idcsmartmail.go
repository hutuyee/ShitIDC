// 智简魔方官方邮件平台（对应魔方 public/plugins/mail/idcsmartmail，明文插件）。
//
// POST {APIBase}/emailapi.php?action=send，multipart/form-data：
//
//	头：api: {AppId}、key: {AppKey}
//	体：email / subject / content / from / from_name
//
// 成功判据：响应 JSON 的 status == 200（数字），失败取 msg。
// 参考实现的 from 只填 @ 前面的部分（官方统一用 mailnoticesystem.com 域名），
// 本实现原样透传，不做拼域。
//
// 有意差异：参考实现把本机 email 附件目录的文件作为 multipart 文件域上传；
// 站内发送链路（mail.send 队列载荷）没有附件字段，attachments 不落地。
// 参考实现固定 http:// 端点（明文传输），本实现保持一致以对齐行为，
// 但允许测试与私有部署通过 APIBase 覆盖。
package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

// Idcsmartmail 实现智简魔方邮件平台通道。
type Idcsmartmail struct {
	// APIBase 可覆盖，便于测试指向本地假服务；默认与参考实现一致（http）。
	APIBase string
}

// NewIdcsmartmail 构造通道。
func NewIdcsmartmail() *Idcsmartmail { return &Idcsmartmail{APIBase: "http://api1.idcsmart.com/"} }

// Name 返回通道标识。
func (m *Idcsmartmail) Name() string { return "idcsmartmail" }

func init() { Register(NewIdcsmartmail()) }

// Validate 检查必填配置。
func (m *Idcsmartmail) Validate(cfg Config, secret Secret) error {
	if cfg.Field("api") == "" {
		return fmt.Errorf("智简魔方邮件需要 AppId（api）")
	}
	if secret.Get("key") == "" {
		return fmt.Errorf("智简魔方邮件需要 AppKey（key）")
	}
	if cfg.Field("from") == "" {
		return fmt.Errorf("智简魔方邮件需要发件人（from）")
	}
	return nil
}

// Send 调用官方邮件平台接口。
func (m *Idcsmartmail) Send(ctx context.Context, cfg Config, secret Secret, msg Message) error {
	if err := m.Validate(cfg, secret); err != nil {
		return err
	}
	apiBase := strings.TrimRight(m.APIBase, "/") + "/emailapi.php?action=send"

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	setField := func(name, value string) {
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"`, name))
		w, _ := mw.CreatePart(h)
		_, _ = w.Write([]byte(value))
	}
	setField("email", msg.To)
	setField("subject", msg.Subject)
	setField("content", msg.HTML)
	setField("from", cfg.Field("from"))
	if fromName := cfg.Field("from_name"); fromName != "" {
		setField("from_name", fromName)
	}
	if err := mw.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiBase, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("api", cfg.Field("api"))
	req.Header.Set("key", secret.Get("key"))
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("智简魔方邮件请求失败: %w", err)
	}
	defer resp.Body.Close()
	var out struct {
		Status any    `json:"status"` // 参考实现按数字比较，这里宽容处理 "200" 与 200
		Msg    string `json:"msg"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("智简魔方邮件返回无法解析(HTTP %d)", resp.StatusCode)
	}
	if !idcsmartMailOK(out.Status) {
		detail := firstNonEmpty(out.Msg, "未知错误")
		return fmt.Errorf("智简魔方邮件发送失败：%s", detail)
	}
	return nil
}

// idcsmartMailOK 对齐 PHP 松散比较 $result['status']==200：数字 200 与字符串 "200" 都算成功。
func idcsmartMailOK(v any) bool {
	switch t := v.(type) {
	case float64:
		return t == 200
	case string:
		return strings.TrimSpace(t) == "200"
	case json.Number:
		n, err := strconv.ParseInt(t.String(), 10, 64)
		return err == nil && n == 200
	}
	return false
}
