package sms

import (
	"io"
	"net/http"
)

// writeStr 是测试里写响应的小工具（http.ResponseWriter 没有 WriteString）。
func writeStr(w http.ResponseWriter, s string) {
	_, _ = io.WriteString(w, s)
}

// readBody 读请求体并转成字符串。
func readBody(r *http.Request) string {
	b, _ := io.ReadAll(r.Body)
	return string(b)
}
