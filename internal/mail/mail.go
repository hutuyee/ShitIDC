package mail

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

type Options struct {
	Host       string `json:"smtp_host"`
	Port       int    `json:"smtp_port"`
	Username   string `json:"smtp_username"`
	Password   string `json:"-"` // never serialized; populated from encrypted storage
	From       string `json:"smtp_from"`
	Encryption string `json:"smtp_encryption"` // "", "none", "starttls", "ssl"
}

func (o Options) Enabled() bool {
	return strings.TrimSpace(o.Host) != "" && strings.TrimSpace(o.From) != "" && o.Port > 0
}

func (o Options) addr() string {
	if o.Port == 0 {
		o.Port = 587
	}
	return fmt.Sprintf("%s:%d", o.Host, o.Port)
}

// Send delivers an HTML mail. Encryption defaults to STARTTLS when unset.
func (o Options) Send(ctx context.Context, to, subject, htmlBody string) error {
	if !o.Enabled() {
		return fmt.Errorf("SMTP 未配置")
	}
	if o.Port == 0 {
		o.Port = 587
	}
	from := o.From
	fromAddr := o.From
	if i := strings.LastIndex(from, "<"); i >= 0 && strings.HasSuffix(from, ">") {
		fromAddr = strings.Trim(from[i+1:], "<>")
	}

	header := make(map[string]string)
	header["From"] = from
	header["To"] = to
	header["Subject"] = mimeWord(subject)
	header["MIME-Version"] = "1.0"
	header["Content-Type"] = "text/html; charset=\"UTF-8\""
	header["Date"] = time.Now().Format(time.RFC1123Z)
	msg := &strings.Builder{}
	keys := make([]string, 0, len(header))
	for k := range header {
		keys = append(keys, k)
	}
	sortStrings(keys)
	for _, k := range keys {
		msg.WriteString(k + ": " + header[k] + "\r\n")
	}
	msg.WriteString("\r\n" + htmlBody)

	timeout := 15 * time.Second
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	dialer := &net.Dialer{Timeout: timeout}

	var conn net.Conn
	var err error
	switch strings.ToLower(o.Encryption) {
	case "ssl", "smtps":
		conn, err = tls.DialWithDialer(dialer, "tcp", o.addr(), &tls.Config{ServerName: o.Host})
	case "none", "plain":
		conn, err = dialer.DialContext(ctx, "tcp", o.addr())
	default: // starttls
		conn, err = dialer.DialContext(ctx, "tcp", o.addr())
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器失败: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	client, err := smtp.NewClient(conn, o.Host)
	if err != nil {
		return fmt.Errorf("SMTP 握手失败: %w", err)
	}
	defer client.Close()

	if strings.EqualFold(o.Encryption, "starttls") || (o.Encryption == "" && okSTARTTLS(client)) {
		if okSTARTTLS(client) {
			if err = client.StartTLS(&tls.Config{ServerName: o.Host}); err != nil {
				return fmt.Errorf("STARTTLS 失败: %w", err)
			}
		}
	}
	if o.Username != "" {
		auth := smtp.PlainAuth("", o.Username, o.Password, o.Host)
		if err = client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err = client.Mail(fromAddr); err != nil {
		return fmt.Errorf("设置发件人失败: %w", err)
	}
	if err = client.Rcpt(to); err != nil {
		return fmt.Errorf("设置收件人失败: %w", err)
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	if _, err = w.Write([]byte(msg.String())); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func okSTARTTLS(c *smtp.Client) bool {
	ok, _ := c.Extension("STARTTLS")
	return ok
}

func mimeWord(s string) string {
	if isASCII(s) {
		return s
	}
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] > 0x7f {
			return false
		}
	}
	return true
}

func sortStrings(v []string) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}

func VerificationMail(code string) (string, string) {
	subject := "ShitIDC 注册验证码"
	body := fmt.Sprintf(`<div style="max-width:520px;margin:0 auto;font-family:sans-serif">
<h2 style="color:#4f46e5">注册验证码</h2>
<p>你的验证码是：</p>
<p style="font-size:30px;font-weight:800;letter-spacing:6px;color:#111">%s</p>
<p>验证码 10 分钟内有效。如果不是你本人操作，请忽略本邮件。</p>
</div>`, code)
	return subject, body
}
