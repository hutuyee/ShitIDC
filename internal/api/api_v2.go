package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/extension"
	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/model"
	"github.com/hutuyee/ShitIDC/internal/payment"
	"github.com/hutuyee/ShitIDC/internal/security"
	"github.com/hutuyee/ShitIDC/internal/store"
	"github.com/hutuyee/ShitIDC/internal/theme"
)

// V2 handlers: coupons, agent groups, referrals, statistics, notifications,
// attachments, mail templates, currencies, extensions, themes, branding,
// gateway refunds and CSV exports.

// ---- coupons (优惠系统) ----

func (a *App) adminListCoupons(c *gin.Context) {
	v, err := a.Store.ListCoupons(c, 200)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取优惠券失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminCreateCoupon(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Code           string     `json:"code"`
		Type           string     `json:"type"` // fixed | percent
		Value          int64      `json:"value"`
		MaxUses        *int       `json:"max_uses"`
		MaxUsesPerUser int        `json:"max_uses_per_user"`
		MinAmountCents int64      `json:"min_amount_cents"`
		ProductIDs     []string   `json:"product_ids"`
		StartsAt       *time.Time `json:"starts_at"`
		ExpiresAt      *time.Time `json:"expires_at"`
		Active         *bool      `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Code = strings.TrimSpace(in.Code)
	if len(in.Code) < 3 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "优惠码至少 3 个字符")
		return
	}
	if in.Type != "fixed" && in.Type != "percent" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "类型仅支持 fixed / percent")
		return
	}
	if in.Value <= 0 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "优惠值必须大于 0")
		return
	}
	if in.Type == "percent" && in.Value > 100 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "百分比折扣不能超过 100")
		return
	}
	if in.MaxUsesPerUser <= 0 {
		in.MaxUsesPerUser = 1
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	// An empty selection means "applies to every product"; drop blanks so a
	// stray "" from the form never becomes a bogus scope entry.
	scope := make([]string, 0, len(in.ProductIDs))
	for _, id := range in.ProductIDs {
		if trimmed := strings.TrimSpace(id); trimmed != "" {
			scope = append(scope, trimmed)
		}
	}
	v, err := a.Store.CreateCoupon(c, in.Code, in.Type, in.Value, in.MaxUses, in.MaxUsesPerUser, in.MinAmountCents, scope, in.StartsAt, in.ExpiresAt, active)
	if err != nil {
		httpx.Fail(c, 400, "COUPON_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "coupon.create", "coupon", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, v)
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdateCoupon(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		MaxUses        *int       `json:"max_uses"`
		MinAmountCents *int64     `json:"min_amount_cents"`
		ExpiresAt      *time.Time `json:"expires_at"`
		Active         *bool      `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.UpdateCoupon(c, c.Param("id"), in.MaxUses, in.MinAmountCents, in.ExpiresAt, in.Active); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "COUPON_NOT_FOUND", "优惠券不存在")
			return
		}
		httpx.Fail(c, 400, "COUPON_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "coupon.update", "coupon", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminDeleteCoupon(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteCoupon(c, c.Param("id")); err != nil {
		httpx.Fail(c, 404, "COUPON_NOT_FOUND", "优惠券不存在")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "coupon.delete", "coupon", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// validateCoupon previews a discount for the buyer's dialog.
func (a *App) validateCoupon(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Code         string `json:"code"`
		ProductID    string `json:"product_id"`
		BillingCycle string `json:"billing_cycle"`
		Quantity     int    `json:"quantity"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	prod, err := a.Store.GetProductPrice(c, in.ProductID, in.BillingCycle)
	if err != nil {
		httpx.Fail(c, 404, "PRODUCT_NOT_FOUND", "产品或周期不存在")
		return
	}
	discount, err := a.Store.ValidateCoupon(c, in.Code, p.User.ID, in.ProductID, prod.PriceCents*int64(in.Quantity))
	if err != nil {
		httpx.Fail(c, 400, "COUPON_INVALID", err.Error())
		return
	}
	httpx.OK(c, 200, map[string]any{"discount_cents": discount, "code": strings.ToLower(strings.TrimSpace(in.Code))})
}

// ---- agent user groups (代理系统) ----

func (a *App) adminListUserGroups(c *gin.Context) {
	v, err := a.Store.ListUserGroups(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取用户组失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminCreateUserGroup(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name            string `json:"name"`
		DiscountPercent int    `json:"discount_percent"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "用户组名称不能为空")
		return
	}
	v, err := a.Store.CreateUserGroup(c, strings.TrimSpace(in.Name), in.DiscountPercent)
	if err != nil {
		httpx.Fail(c, 400, "GROUP_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "user_group.create", "user_group", v["id"].(string), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, v)
	httpx.OK(c, 201, v)
}

func (a *App) adminUpdateUserGroup(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name            string `json:"name"`
		DiscountPercent int    `json:"discount_percent"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.UpdateUserGroup(c, c.Param("id"), strings.TrimSpace(in.Name), in.DiscountPercent); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "GROUP_NOT_FOUND", "用户组不存在")
			return
		}
		httpx.Fail(c, 400, "GROUP_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "user_group.update", "user_group", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminDeleteUserGroup(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteUserGroup(c, c.Param("id")); err != nil {
		httpx.Fail(c, 404, "GROUP_NOT_FOUND", "用户组不存在")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "user_group.delete", "user_group", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminSetUserGroup(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		GroupID string `json:"group_id"` // "" = clear
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SetUserGroup(c, c.Param("id"), strings.TrimSpace(in.GroupID)); err != nil {
		httpx.Fail(c, 400, "USER_GROUP_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "user_group.assign", "user", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"group_id": in.GroupID})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// myReferral returns the caller's invite code, stats and commission records.
func (a *App) myReferral(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.ReferralInfo(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取推广信息失败")
		return
	}
	if settings, err := a.Store.GetReferralSettings(c); err == nil {
		v["enabled"] = settings.Enabled
		v["percent"] = settings.Percent
	}
	httpx.OK(c, 200, v)
}

// ---- financial statistics (财务统计) ----

func (a *App) adminStatistics(c *gin.Context) {
	v, err := a.Store.FinancialStatistics(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取统计失败")
		return
	}
	if sf, err := a.Store.GetStorefrontSettings(c); err == nil && sf.BaseCurrency != "" {
		v.BaseCurrency = sf.BaseCurrency
	} else {
		v.BaseCurrency = "CNY"
	}
	httpx.OK(c, 200, v)
}

// ---- notifications (站内通知) ----

func (a *App) listNotifications(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.ListNotifications(c, p.User.ID, 50)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取通知失败")
		return
	}
	unread, _ := a.Store.UnreadNotificationCount(c, p.User.ID)
	httpx.OK(c, 200, map[string]any{"items": v, "unread": unread})
}

func (a *App) markNotification(c *gin.Context) {
	p, _ := getPrincipal(c)
	id := c.Param("id")
	if id == "" {
		id = "all"
	}
	if err := a.Store.MarkNotificationRead(c, p.User.ID, id); err != nil {
		httpx.Fail(c, 400, "NOTIFY_MARK_FAILED", "标记已读失败")
		return
	}
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- ticket attachments (工单附件, §43) ----

var attachmentExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".pdf": true, ".zip": true, ".txt": true, ".log": true, ".json": true}

// attachmentMagic sniffs the leading bytes of common upload types (§43).
func attachmentMagic(name string, head []byte) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png":
		return len(head) >= 4 && head[0] == 0x89 && head[1] == 'P' && head[2] == 'N' && head[3] == 'G'
	case ".jpg", ".jpeg":
		return len(head) >= 3 && head[0] == 0xFF && head[1] == 0xD8 && head[2] == 0xFF
	case ".gif":
		return len(head) >= 4 && string(head[:4]) == "GIF8"
	case ".pdf":
		return len(head) >= 4 && string(head[:4]) == "%PDF"
	case ".zip":
		return len(head) >= 4 && head[0] == 'P' && head[1] == 'K'
	default:
		return true // txt / log / json: content checks are best effort
	}
}

func (a *App) uploadTicketAttachment(c *gin.Context) {
	p, _ := getPrincipal(c)
	ticketID := c.Param("id")
	fileHeader, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, 400, "FILE_REQUIRED", "请选择要上传的文件")
		return
	}
	ext := strings.ToLower(filepath.Ext(fileHeader.Filename))
	if !attachmentExts[ext] {
		httpx.Fail(c, 400, "FILE_TYPE_REJECTED", "不支持的文件类型")
		return
	}
	if fileHeader.Size > 5<<20 {
		httpx.Fail(c, 400, "FILE_TOO_LARGE", "附件不能超过 5 MB")
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		httpx.Fail(c, 400, "FILE_INVALID", "文件读取失败")
		return
	}
	defer f.Close()
	content, err := io.ReadAll(io.LimitReader(f, 5<<20))
	if err != nil || len(content) == 0 {
		httpx.Fail(c, 400, "FILE_INVALID", "文件内容为空")
		return
	}
	if !attachmentMagic(fileHeader.Filename, content) {
		httpx.Fail(c, 400, "FILE_CONTENT_REJECTED", "文件内容与扩展名不符")
		return
	}
	// Is the caller allowed to touch this ticket? (owner or staff)
	owner := p.User.ID
	if p.Permissions["ticket.manage"] {
		owner = 0
	}
	if _, err := a.Store.GetTicket(c, ticketID, owner); err != nil {
		httpx.Fail(c, 404, "TICKET_NOT_FOUND", "工单不存在")
		return
	}
	dir := filepath.Join(a.Cfg.Storage.Dir, "uploads", "tickets", ticketID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "存储目录创建失败")
		return
	}
	stored := uuid.NewString() + ext
	full := filepath.Join(dir, stored)
	if err := os.WriteFile(full, content, 0o644); err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "附件保存失败")
		return
	}
	// 配了对象存储通道就把附件转存过去：成功后删本机文件，入库记 oss: 前缀。
	storedRef := stored
	if target, oerr := a.activeOSSTarget(c); oerr == nil && target != nil {
		key := "uploads/tickets/" + ticketID + "/" + stored
		acl := "private"
		switch ext {
		case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".pdf":
			acl = "public-read"
		}
		if uerr := target.impl.Upload(c, target.cfg, target.secret, key, content, mime.TypeByExtension(ext), acl); uerr == nil {
			_ = os.Remove(full)
			storedRef = "oss:" + key
		}
	}
	att, err := a.Store.InsertAttachment(c, ticketID, p.User.ID, filepath.Base(fileHeader.Filename), storedRef, ext, int64(len(content)))
	if err != nil {
		_ = os.Remove(full)
		httpx.Fail(c, 400, "ATTACHMENT_FAILED", err.Error())
		return
	}
	httpx.OK(c, 201, att)
}

func (a *App) downloadAttachment(c *gin.Context) {
	p, _ := getPrincipal(c)
	userID := p.User.ID
	if p.Permissions["ticket.manage"] {
		userID = 0 // staff may read any ticket's attachments
	}
	att, storedPath, err := a.Store.GetAttachmentForRead(c, userID, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 404, "ATTACHMENT_NOT_FOUND", "附件不存在")
		return
	}
	// 对象存储附件：302 到 3 分钟有效的签名地址。
	if strings.HasPrefix(storedPath, "oss:") {
		key := strings.TrimPrefix(storedPath, "oss:")
		target, oerr := a.activeOSSTarget(c)
		if oerr != nil || target == nil {
			httpx.Fail(c, 502, "ATTACHMENT_UNAVAILABLE", "对象存储通道不可用")
			return
		}
		signed, serr := target.impl.SignedURL(c, target.cfg, target.secret, key, 3*time.Minute)
		if serr != nil {
			httpx.Fail(c, 502, "ATTACHMENT_UNAVAILABLE", "生成下载地址失败")
			return
		}
		c.Redirect(http.StatusFound, signed)
		return
	}
	full := filepath.Join(a.Cfg.Storage.Dir, "uploads", "tickets", att.TicketID, storedPath)
	if !strings.HasPrefix(filepath.Clean(full), filepath.Clean(filepath.Join(a.Cfg.Storage.Dir, "uploads"))+string(os.PathSeparator)) {
		httpx.Fail(c, 400, "ATTACHMENT_INVALID", "附件路径非法")
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		httpx.Fail(c, 404, "ATTACHMENT_NOT_FOUND", "附件文件已丢失")
		return
	}
	// Force download semantics: never render uploads inline (XSS hardening).
	c.Header("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(att.Filename, "\"", "_")+"\"")
	c.Data(http.StatusOK, "application/octet-stream", data)
}

// ---- mail templates (邮件模板) ----

func (a *App) adminListMailTemplates(c *gin.Context) {
	v, err := a.Store.ListMailTemplates(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取邮件模板失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminSaveMailTemplate(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Name    string `json:"name"`
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	known := map[string]bool{"email_verification": true, "password_reset": true, "login_notify": true, "ticket_user": true, "ticket_staff": true}
	if !known[in.Name] {
		httpx.Fail(c, 400, "INVALID_TEMPLATE", "模板名必须是 email_verification / password_reset / login_notify / ticket_user / ticket_staff")
		return
	}
	if err := a.Store.SaveMailTemplate(c, in.Name, in.Subject, in.Body); err != nil {
		httpx.Fail(c, 500, "TEMPLATE_SAVE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "settings.mail_template.save", "settings", in.Name, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminDeleteMailTemplate(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteMailTemplate(c, c.Param("name")); err != nil {
		httpx.Fail(c, 404, "TEMPLATE_NOT_FOUND", "模板不存在")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "settings.mail_template.delete", "settings", c.Param("name"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- currencies (多币种) ----

func (a *App) listCurrencies(c *gin.Context) {
	v, err := a.Store.ListCurrencies(c, true)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取货币失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminListCurrencies(c *gin.Context) {
	v, err := a.Store.ListCurrencies(c, false)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取货币失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminSaveCurrency(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Code   string `json:"code"`
		Rate   string `json:"rate"`
		Symbol string `json:"symbol"`
		Active bool   `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.Code = strings.TrimSpace(strings.ToUpper(in.Code))
	if len(in.Code) != 3 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "货币代码必须是 3 位字母")
		return
	}
	if _, err := strconv.ParseFloat(in.Rate, 64); err != nil || in.Rate == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "汇率必须是数字（相对基础货币）")
		return
	}
	if err := a.Store.SaveCurrency(c, in.Code, in.Rate, in.Symbol, in.Active); err != nil {
		httpx.Fail(c, 400, "CURRENCY_SAVE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "currency.save", "currency", in.Code, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminDeleteCurrency(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeleteCurrency(c, c.Param("code")); err != nil {
		httpx.Fail(c, 400, "CURRENCY_DELETE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "currency.delete", "currency", c.Param("code"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- WASM extensions (第十/二十四阶段) ----

func (a *App) adminListExtensions(c *gin.Context) {
	v, err := a.Store.ListExtensions(c)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取扩展失败")
		return
	}
	out := []map[string]any{}
	for _, e := range v {
		out = append(out, map[string]any{"id": e.PublicID, "name": e.Name, "version": e.Version, "description": e.Description, "permissions": e.Permissions, "active": e.Active, "loaded": a.ExtHost != nil && a.extHostLoaded(e.Name)})
	}
	httpx.OK(c, 200, out)
}

func (a *App) adminUploadExtension(c *gin.Context) {
	p, _ := getPrincipal(c)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, 400, "FILE_REQUIRED", "请上传扩展包 zip")
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		httpx.Fail(c, 400, "FILE_INVALID", "文件读取失败")
		return
	}
	defer f.Close()
	v, err := a.installExtensionZip(c, f)
	if err != nil {
		httpx.Fail(c, 400, "PACKAGE_INVALID", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "extension.upload", "extension", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": v.Name, "version": v.Version, "permissions": v.Permissions})
	httpx.OK(c, 201, v)
}

func (a *App) adminSetExtensionActive(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Active bool `json:"active"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.SetExtensionActive(c, c.Param("id"), in.Active)
	if err != nil {
		httpx.Fail(c, 404, "EXTENSION_NOT_FOUND", "扩展不存在")
		return
	}
	if a.ExtHost != nil {
		if in.Active {
			if wasm, err := os.ReadFile(v.SourcePath); err == nil {
				if _, err := a.ExtHost.Load(context.Background(), extension.Manifest{Name: v.Name, Version: v.Version, Description: v.Description, Entry: filepath.Base(v.SourcePath), Permissions: v.Permissions, Events: nil}, wasm); err != nil {
					httpx.Fail(c, 500, "EXTENSION_LOAD_FAILED", err.Error())
					return
				}
			}
		} else {
			a.ExtHost.Unload(v.Name)
		}
	}
	_ = a.Store.Audit(c, p.User.ID, "extension.toggle", "extension", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"active": in.Active})
	httpx.OK(c, 200, v)
}

func (a *App) adminDeleteExtension(c *gin.Context) {
	p, _ := getPrincipal(c)
	v, err := a.Store.DeleteExtension(c, c.Param("id"))
	if err != nil {
		httpx.Fail(c, 404, "EXTENSION_NOT_FOUND", "扩展不存在")
		return
	}
	if a.ExtHost != nil {
		a.ExtHost.Unload(v.Name)
	}
	_ = os.RemoveAll(filepath.Dir(v.SourcePath))
	_ = a.Store.Audit(c, p.User.ID, "extension.delete", "extension", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminExtensionLogs(c *gin.Context) {
	if a.ExtHost == nil {
		httpx.OK(c, 200, map[string][]string{})
		return
	}
	out := map[string][]string{}
	for _, name := range a.ExtHost.Loaded() {
		if inst := a.extHostInstance(name); inst != nil {
			out[name] = inst.Logs
		}
	}
	httpx.OK(c, 200, out)
}

// extHostLoaded / extHostInstance are small adapters over the host.
func (a *App) extHostLoaded(name string) bool {
	for _, n := range a.ExtHost.Loaded() {
		if n == name {
			return true
		}
	}
	return false
}

func (a *App) extHostInstance(name string) *extension.Instance {
	if a.ExtHost == nil {
		return nil
	}
	return a.ExtHost.Instance(name)
}

// ---- themes (第九/二十阶段) ----

func (a *App) adminListThemes(c *gin.Context) {
	v, err := theme.List(a.Cfg.Storage.ThemeDir())
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取主题失败")
		return
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminUploadTheme(c *gin.Context) {
	p, _ := getPrincipal(c)
	fileHeader, err := c.FormFile("file")
	if err != nil {
		httpx.Fail(c, 400, "FILE_REQUIRED", "请上传主题包 zip")
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		httpx.Fail(c, 400, "FILE_INVALID", "文件读取失败")
		return
	}
	defer f.Close()
	pkg, err := theme.Install(a.Cfg.Storage.ThemeDir(), readAllLimit(c, f, 20<<20))
	if err != nil {
		httpx.Fail(c, 400, "THEME_INVALID", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "theme.install", "theme", pkg.ID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, pkg)
	httpx.OK(c, 201, pkg)
}

func (a *App) adminDeleteTheme(c *gin.Context) {
	p, _ := getPrincipal(c)
	id := c.Param("id")
	if sf, err := a.Store.GetStorefrontSettings(c); err == nil && sf.ActiveTheme == id {
		httpx.Fail(c, 409, "THEME_ACTIVE", "请先切换到其他主题再删除")
		return
	}
	if err := theme.Delete(a.Cfg.Storage.ThemeDir(), id); err != nil {
		httpx.Fail(c, 404, "THEME_NOT_FOUND", "主题不存在或不可删除")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "theme.delete", "theme", id, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func readAllLimit(c *gin.Context, f multipart.File, limit int64) []byte {
	data, _ := io.ReadAll(io.LimitReader(f, limit))
	return data
}

// ---- storefront / branding settings ----

func (a *App) branding(c *gin.Context) {
	v, err := a.Store.GetBranding(c)
	if err != nil {
		v = store.BrandingSettings{}
	}
	httpx.OK(c, 200, map[string]any{"site_name": v.SiteName, "logo_url": v.LogoURL, "primary_color": v.PrimaryColor})
}

func (a *App) adminGetBranding(c *gin.Context) {
	v, err := a.Store.GetBranding(c)
	if err != nil {
		v = store.BrandingSettings{}
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminSaveBranding(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in store.BrandingSettings
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if len(in.PrimaryColor) > 0 && !strings.HasPrefix(in.PrimaryColor, "#") {
		httpx.Fail(c, 400, "INVALID_REQUEST", "主题色必须是 #RRGGBB")
		return
	}
	if err := a.Store.SaveBranding(c, in); err != nil {
		httpx.Fail(c, 500, "BRANDING_SAVE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "settings.branding", "settings", "branding", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminGetStorefrontSettings(c *gin.Context) {
	v, err := a.Store.GetStorefrontSettings(c)
	if err != nil {
		v = store.StorefrontSettings{}
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminSaveStorefrontSettings(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in store.StorefrontSettings
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if in.ActiveTheme != "" && !theme.ValidID(in.ActiveTheme) {
		httpx.Fail(c, 400, "INVALID_REQUEST", "主题 ID 非法")
		return
	}
	if in.MaxOrdersPerHour < 0 || in.MaxOrdersPerHour > 10000 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "下单限速必须是 0-10000")
		return
	}
	if err := a.Store.SaveStorefrontSettings(c, in); err != nil {
		httpx.Fail(c, 500, "STOREFRONT_SAVE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "settings.storefront", "settings", "storefront", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- referral settings ----

func (a *App) adminGetReferralSettings(c *gin.Context) {
	v, err := a.Store.GetReferralSettings(c)
	if err != nil {
		v = store.ReferralSettings{}
	}
	httpx.OK(c, 200, v)
}

func (a *App) adminSaveReferralSettings(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in store.ReferralSettings
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SaveReferralSettings(c, in); err != nil {
		httpx.Fail(c, 500, "REFERRAL_SAVE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "settings.referral", "settings", "referral", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- gateway refunds (自动退款) ----

// gatewayRefund issues a gateway-side refund for an online payment. It writes
// the error response itself and returns an error for the caller to stop.
func (a *App) gatewayRefund(c *gin.Context, actorID int64, orderID, reason string, info store.OrderPaymentInfo) (model.Refund, error) {
	gw, ok := payment.Get(info.Method)
	if !ok {
		httpx.Fail(c, 400, "GATEWAY_UNKNOWN", "支付渠道未实现: "+info.Method)
		return model.Refund{}, fmt.Errorf("gateway unknown")
	}
	refunder, ok := gw.(payment.Refunder)
	if !ok {
		httpx.Fail(c, 400, "GATEWAY_REFUND_UNSUPPORTED", "该支付渠道不支持自动退款，请在网关后台操作后用钱包冲正处理")
		return model.Refund{}, fmt.Errorf("refund unsupported")
	}
	if len(a.Cfg.MasterKey) == 0 {
		httpx.Fail(c, 503, "MASTER_KEY_REQUIRED", "需要 MASTER_KEY_BASE64 解密支付密钥")
		return model.Refund{}, fmt.Errorf("master key required")
	}
	pv, secretEnc, err := a.Store.GetPaymentProviderByMethod(c, info.Method)
	if err != nil {
		httpx.Fail(c, 400, "GATEWAY_CONFIG_MISSING", "找不到该渠道的支付方式配置")
		return model.Refund{}, err
	}
	secret, err := security.Decrypt(a.Cfg.MasterKey, secretEnc)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "支付密钥解密失败")
		return model.Refund{}, err
	}
	cfg := payment.ProviderConfig{Method: pv.Method, GatewayURL: pv.GatewayURL, MerchantID: pv.MerchantID, Secret: secret}
	ctx, cancel := context.WithTimeout(c, 30*time.Second)
	defer cancel()
	gatewayRefundID, err := refunder.Refund(ctx, cfg, payment.RefundRequest{OutTradeNo: info.TransactionID, TradeNo: info.TransactionID, AmountCents: info.AmountCents, RefundID: uuid.NewString(), Reason: reason})
	if err != nil {
		_ = a.Store.SecurityEvent(c, 0, "refund.gateway_failed", "warning", clientIP(c), map[string]any{"order": orderID, "method": info.Method, "error": err.Error()})
		httpx.Fail(c, 502, "GATEWAY_REFUND_FAILED", err.Error())
		return model.Refund{}, err
	}
	r, err := a.Store.RecordGatewayRefund(c, actorID, orderID, reason, c.GetString("request_id"), gatewayRefundID)
	if err != nil {
		httpx.Fail(c, 400, "REFUND_FAILED", err.Error())
		return model.Refund{}, err
	}
	a.Bus.Emit(a.eventCtx(c), events.OrderRefunded, map[string]any{"order_id": r.OrderID, "uid": 0, "amount_cents": r.AmountCents, "refund_id": r.PublicID, "gateway": info.Method})
	return r, nil
}

// ---- CSV exports ----

func (a *App) adminExportUsers(c *gin.Context) {
	rows, err := a.Store.ListUsers(c, c.Query("query"), 500)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取用户失败")
		return
	}
	header := []string{"uid", "id", "email", "status", "email_verified", "balance_cents", "currency", "order_count", "created_at", "last_login_at"}
	var sb strings.Builder
	writeCSVRow(&sb, header)
	for _, u := range rows {
		last := ""
		if u.LastLoginAt != nil {
			last = u.LastLoginAt.Format(time.RFC3339)
		}
		writeCSVRow(&sb, []string{strconv.FormatInt(u.UID, 10), u.PublicID, u.Email, u.Status, strconv.FormatBool(u.EmailVerified), strconv.FormatInt(u.BalanceCents, 10), u.Currency, strconv.FormatInt(u.OrderCount, 10), u.CreatedAt.Format(time.RFC3339), last})
	}
	c.Header("Content-Disposition", "attachment; filename=users.csv")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(sb.String()))
}

func (a *App) adminExportOrders(c *gin.Context) {
	rows, err := a.Store.ListOrdersAdmin(c, 500)
	if err != nil {
		httpx.Fail(c, 500, "INTERNAL_ERROR", "读取订单失败")
		return
	}
	header := []string{"id", "user_uid", "status", "kind", "total_cents", "discount_cents", "coupon_code", "currency", "created_at", "paid_at"}
	var sb strings.Builder
	writeCSVRow(&sb, header)
	for _, o := range rows {
		paid := ""
		if o.PaidAt != nil {
			paid = o.PaidAt.Format(time.RFC3339)
		}
		writeCSVRow(&sb, []string{o.PublicID, strconv.FormatInt(o.UserUID, 10), o.Status, o.Kind, strconv.FormatInt(o.TotalCents, 10), strconv.FormatInt(o.DiscountCents, 10), o.CouponCode, o.Currency, o.CreatedAt.Format(time.RFC3339), paid})
	}
	c.Header("Content-Disposition", "attachment; filename=orders.csv")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", []byte(sb.String()))
}

func writeCSVRow(sb *strings.Builder, cells []string) {
	for i, cell := range cells {
		if i > 0 {
			sb.WriteByte(',')
		}
		if strings.ContainsAny(cell, ",\"\n\r") {
			cell = "\"" + strings.ReplaceAll(cell, "\"", "\"\"") + "\""
		}
		sb.WriteString(cell)
	}
	sb.WriteByte('\n')
}
