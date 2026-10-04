package httpx

import "github.com/gin-gonic/gin"

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Response struct {
	Data      any       `json:"data"`
	Error     *APIError `json:"error"`
	RequestID string    `json:"request_id"`
}

func OK(c *gin.Context, status int, data any) {
	c.Set("error_code", "")
	c.JSON(status, Response{Data: data, Error: nil, RequestID: requestID(c)})
}

func Fail(c *gin.Context, status int, code, message string) {
	// Picked up by the api_logs middleware (§5.6) — the body is not parsed twice.
	c.Set("error_code", code)
	c.JSON(status, Response{Data: nil, Error: &APIError{Code: code, Message: message}, RequestID: requestID(c)})
}

func requestID(c *gin.Context) string {
	if v, ok := c.Get("request_id"); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
