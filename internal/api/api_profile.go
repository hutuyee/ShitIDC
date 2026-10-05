package api

import (
	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/model"
)

// getProfile returns the caller's editable profile plus the fixed identity
// fields (email is the login identity and is never editable here).
// verified_phone 是绑定在账号上的手机号（短信验证码绑定），与资料里的
// 联系电话是两回事；前端用它渲染绑定状态。
func (a *App) getProfile(c *gin.Context) {
	p, _ := getPrincipal(c)
	prof, err := a.Store.GetProfile(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取个人资料失败")
		return
	}
	verifiedPhone, phoneVerified, _ := a.Store.GetUserVerifiedPhone(c, p.User.ID)
	httpx.OK(c, 200, map[string]any{
		"uid":            p.User.ID,
		"email":          p.User.Email,
		"profile":        prof,
		"verified_phone": verifiedPhone,
		"phone_verified": phoneVerified,
	})
}

// updateProfile saves the caller's own profile. Every field is optional;
// blanks clear the stored value except country which falls back to 中国.
func (a *App) updateProfile(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in model.UserProfile
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	fields := map[string]string{
		"nickname": in.Nickname, "real_name": in.RealName, "company": in.Company,
		"phone": in.Phone, "qq": in.QQ, "country": in.Country,
		"province": in.Province, "city": in.City, "address": in.Address,
	}
	for name, value := range fields {
		if len([]rune(value)) > 200 {
			httpx.Fail(c, 400, "FIELD_TOO_LONG", name+" 最长 200 个字符")
			return
		}
	}
	if err := a.Store.SaveProfile(c, p.User.ID, in); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "保存个人资料失败")
		return
	}
	saved, err := a.Store.GetProfile(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取个人资料失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "profile.update", "user", p.User.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"nickname": saved.Nickname})
	httpx.OK(c, 200, map[string]any{
		"uid":     p.User.ID,
		"email":   p.User.Email,
		"profile": saved,
	})
}
