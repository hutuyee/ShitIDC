package oauth

import (
	"io"
	"net/http"
)

// writeStr 是测试里写响应的小工具（http.ResponseWriter 没有 WriteString）。
func writeStr(w http.ResponseWriter, s string) {
	_, _ = io.WriteString(w, s)
}
