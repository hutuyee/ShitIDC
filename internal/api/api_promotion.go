package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 活动促销（对齐魔方 CBAP 插件 EventPromotion）。
//
// 后台接口沿用插件前端契约（template/admin/api/index.js）：
//   GET    /event_promotion           列表（keywords/status/page/limit/time）
//   GET    /event_promotion/:id       详情（data.event_promotion）
//   POST   /event_promotion           新增   PUT /:id 修改   DELETE /:id 删除
//   PUT    /event_promotion/:id/status  启停（status: 1 启用 / 0 停用）
//   GET    /event_promotion/active      启用中活动 + 配置（供排序弹窗）
//   PUT    /event_promotion/order       排序（id: [...]）
//   PUT    /event_promotion/config      配置（addon_event_promotion_does_not_participate）
//
// 金额字段沿用插件的「元」，时间沿用 Unix 秒；落库一律换成「分」与 TIMESTAMPTZ。

// flexBool 兼容插件的 0/1 开关与我们自己的 true/false。
type flexBool bool

func (b *flexBool) UnmarshalJSON(raw []byte) error {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "true":
		*b = true
		return nil
	case "false", "null", "":
		*b = false
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("开关取值不合法")
	}
	*b = f != 0
	return nil
}

type promotionForm struct {
	Name           string   `json:"name"`
	Type           string   `json:"type"`
	Value          float64  `json:"value"`
	Full           float64  `json:"full"`
	StartTime      int64    `json:"start_time"`
	EndTime        int64    `json:"end_time"`
	Products       []string `json:"products"`
	Clients        []string `json:"clients"`
	ClientType     string   `json:"client_type"`
	NewUser        flexBool `json:"new_user"`
	OldUser        flexBool `json:"old_user"`
	SingleUserOnce flexBool `json:"single_user_once"`
	CycleLimit     flexBool `json:"cycle_limit"`
	Cycle          []string `json:"cycle"`
	Notes          string   `json:"notes"`
}

func (f promotionForm) input() store.PromotionInput {
	in := store.PromotionInput{
		Name:             strings.TrimSpace(f.Name),
		Type:             strings.ToLower(strings.TrimSpace(f.Type)),
		FullCents:        int64(math.Round(f.Full * 100)),
		ProductPublicIDs: f.Products,
		ClientType:       strings.ToLower(strings.TrimSpace(f.ClientType)),
		ClientPublicIDs:  f.Clients,
		NewUser:          bool(f.NewUser),
		OldUser:          bool(f.OldUser),
		SingleUserOnce:   bool(f.SingleUserOnce),
		CycleLimit:       bool(f.CycleLimit),
		Cycle:            f.Cycle,
		Notes:            f.Notes,
	}
	if in.Type == "percent" {
		in.PercentValue = f.Value
	} else {
		in.ReduceCents = int64(math.Round(f.Value * 100))
	}
	if f.StartTime > 0 {
		in.StartAt = time.Unix(f.StartTime, 0)
	}
	if f.EndTime > 0 {
		end := time.Unix(f.EndTime, 0)
		in.EndAt = &end
	}
	return in
}

// promotionPayload 把内部结构转成插件字段形状（元 / Unix 秒 / 0-1 开关）。
func promotionPayload(v store.Promotion) gin.H {
	value := 0.0
	if v.Type == "percent" {
		value = v.PercentValue
	} else {
		value = float64(v.ReduceCents) / 100
	}
	endUnix := int64(0)
	if v.EndAt != nil {
		endUnix = v.EndAt.Unix()
	}
	b2i := func(b bool) int {
		if b {
			return 1
		}
		return 0
	}
	return gin.H{
		"id": v.PublicID, "name": v.Name, "active_name": v.Name, "type": v.Type,
		"value": value, "full": float64(v.FullCents) / 100,
		"start_time": v.StartAt.Unix(), "end_time": endUnix,
		"products": v.ProductPublicIDs, "clients": v.ClientPublicIDs, "client_type": v.ClientType,
		"new_user": b2i(v.NewUser), "old_user": b2i(v.OldUser), "single_user_once": b2i(v.SingleUserOnce),
		"cycle_limit": b2i(v.CycleLimit), "cycle": v.Cycle,
		"notes": v.Notes, "status": v.Status, "enabled": v.Enabled,
		"sort_order": v.SortOrder, "created_at": v.CreatedAt,
	}
}

func (a *App) adminListPromotions(c *gin.Context) {
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit <= 0 || limit > 200 {
		limit = 20
	}
	var at *time.Time
	if raw := strings.TrimSpace(c.Query("time")); raw != "" {
		if ts, err := strconv.ParseInt(raw, 10, 64); err == nil && ts > 0 {
			t := time.Unix(ts, 0)
			at = &t
		}
	}
	items, total, err := a.Store.ListPromotions(c, c.Query("keywords"), c.Query("status"), at, limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "PROMOTIONS_FAILED", "读取活动失败")
		return
	}
	list := make([]gin.H, 0, len(items))
	for _, v := range items {
		list = append(list, promotionPayload(v))
	}
	httpx.OK(c, 200, gin.H{"list": list, "count": total, "page": page, "limit": limit})
}

func (a *App) adminGetPromotion(c *gin.Context) {
	v, err := a.Store.GetPromotion(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PROMOTION_NOT_FOUND", "活动不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "PROMOTION_FAILED", "读取活动失败")
		return
	}
	httpx.OK(c, 200, gin.H{"event_promotion": promotionPayload(v)})
}

func (a *App) adminCreatePromotion(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in promotionForm
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	v, err := a.Store.CreatePromotion(c, in.input())
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PROMOTION_SCOPE_NOT_FOUND", "指定的商品或用户不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PROMOTION_CREATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "promotion.create", "promotion", v.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": v.Name, "type": v.Type})
	httpx.OK(c, 201, gin.H{"event_promotion": promotionPayload(v)})
}

func (a *App) adminUpdatePromotion(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in promotionForm
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	err := a.Store.UpdatePromotion(c, c.Param("id"), in.input())
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "PROMOTION_NOT_FOUND", "活动不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 400, "PROMOTION_UPDATE_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "promotion.update", "promotion", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"name": in.Name})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

func (a *App) adminDeletePromotion(c *gin.Context) {
	p, _ := getPrincipal(c)
	if err := a.Store.DeletePromotion(c, c.Param("id")); err != nil {
		httpx.Fail(c, 404, "PROMOTION_NOT_FOUND", "活动不存在")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "promotion.delete", "promotion", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminSetPromotionStatus 启停（status: 1 启用 / 0 停用，与插件一致）。
func (a *App) adminSetPromotionStatus(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		Status *int `json:"status"`
	}
	if err := c.ShouldBindJSON(&in); err != nil || in.Status == nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	enabled := *in.Status == 1
	if err := a.Store.SetPromotionEnabled(c, c.Param("id"), enabled); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpx.Fail(c, 404, "PROMOTION_NOT_FOUND", "活动不存在")
			return
		}
		httpx.Fail(c, 400, "PROMOTION_UPDATE_FAILED", err.Error())
		return
	}
	action := "promotion.disable"
	if enabled {
		action = "promotion.enable"
	}
	_ = a.Store.Audit(c, p.User.ID, action, "promotion", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminActivePromotions 启用中的活动 + 插件配置（排序弹窗数据源）。
func (a *App) adminActivePromotions(c *gin.Context) {
	items, err := a.Store.ListActivePromotions(c)
	if err != nil {
		httpx.Fail(c, 500, "PROMOTIONS_FAILED", "读取活动失败")
		return
	}
	cfg, err := a.Store.GetPromotionConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "PROMOTIONS_FAILED", "读取活动配置失败")
		return
	}
	list := make([]gin.H, 0, len(items))
	for _, v := range items {
		list = append(list, promotionPayload(v))
	}
	httpx.OK(c, 200, gin.H{"list": list, "addon_event_promotion_does_not_participate": cfg.DoesNotParticipate})
}

// adminReorderPromotions 保存排序（数组顺序即优先级）。
func (a *App) adminReorderPromotions(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		ID []string `json:"id"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.ReorderPromotions(c, in.ID); err != nil {
		httpx.Fail(c, 400, "PROMOTION_ORDER_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "promotion.reorder", "promotion", "", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"ids": in.ID})
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// adminSavePromotionConfig 保存插件配置项。
func (a *App) adminSavePromotionConfig(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		DoesNotParticipate *bool `json:"addon_event_promotion_does_not_participate"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	cfg := store.PromotionConfig{}
	if in.DoesNotParticipate != nil {
		cfg.DoesNotParticipate = *in.DoesNotParticipate
	}
	if err := a.Store.SavePromotionConfig(c, cfg); err != nil {
		httpx.Fail(c, 400, "PROMOTION_CONFIG_FAILED", err.Error())
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "promotion.config", "promotion", "", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, json.RawMessage(`{"config":"event_promotion"}`))
	httpx.OK(c, 200, map[string]bool{"ok": true})
}

// ---- 前台：进行中的活动 ----

func (a *App) myRunningPromotions(c *gin.Context) {
	p, _ := getPrincipal(c)
	items, err := a.Store.ListRunningPromotions(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "PROMOTIONS_FAILED", "读取活动失败")
		return
	}
	list := make([]gin.H, 0, len(items))
	for _, v := range items {
		list = append(list, promotionPayload(v))
	}
	httpx.OK(c, 200, list)
}
