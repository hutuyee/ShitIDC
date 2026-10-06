package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 推介计划（对齐魔方 CBAP IdcsmartRecommend 插件）。
//
// 后台接口 /admin/recommend*（权限 wallet.adjust）：奖励记录列表与状态操作
// （确认 / 冻结 / 解冻 / 无效 / 改奖励金额 / 删除）、预设无效回复、提现审核、
// 推介配置（初始奖励存款 / 确认天数 / 最低提现金额 / 提现手续费 / 默认推介链接 /
// 商品新购与续费比例 / 主打推介产品）。
// 用户端接口 /recommend/*：开启推介计划、推介链接（含自定义链接）、建议推介产品、
// 奖励记录、推介政策与提现申请。
// 与旧「推广返佣」（/referral，支付后按订单总额即时返到余额）并存：被推介用户的
// 推荐人开启过推介计划时走本模块的奖励记录，并跳过旧的即时返佣，避免重复发放。

// recommendLinkBase 推介链接跳转页：配置 default_url 优先，缺省站内登录页。
func (a *App) recommendLinkBase(c *gin.Context) string {
	if cfg, err := a.Store.GetRecommendConfig(c); err == nil && strings.TrimSpace(cfg.DefaultURL) != "" {
		return cfg.DefaultURL
	}
	return strings.TrimRight(a.Cfg.PublicBaseURL, "/") + "/login"
}

// recommendProductURL 生成某商品的推介链接（带推介码与商品 ID）。
func (a *App) recommendProductURL(c *gin.Context, userID, productID int64) string {
	code, err := a.Store.EnsureReferralCode(c, userID)
	if err != nil {
		return ""
	}
	out := store.RecommendLinkURL(a.recommendLinkBase(c), code, "")
	if productID > 0 {
		out += "&product_id=" + strconv.FormatInt(productID, 10)
	}
	return out
}

// ---- 用户端 ----

// myRecommendPromoter 推介人概览；未开启时 promoter 为空对象。
func (a *App) myRecommendPromoter(c *gin.Context) {
	p, _ := getPrincipal(c)
	info, err := a.Store.RecommendPromoter(c, p.User.ID, a.recommendLinkBase(c))
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介信息失败")
		return
	}
	httpx.OK(c, 200, gin.H{"promoter": info, "opened": info.UserID != 0})
}

// openRecommendPromoter 开启推介计划（首次开启按配置发放初始奖励存款）。
func (a *App) openRecommendPromoter(c *gin.Context) {
	p, _ := getPrincipal(c)
	first, err := a.Store.EnableRecommendPromoter(c, p.User.ID)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "开启推介计划失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "recommend.open", "recommend", strconv.FormatInt(p.User.ID, 10), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"first": first})
	httpx.OK(c, 200, gin.H{"enabled": true, "first": first})
}

// myRecommendDescription 未开启推介计划页面的宣传信息（开启奖励 + 最高比例）。
func (a *App) myRecommendDescription(c *gin.Context) {
	cfg, err := a.Store.GetRecommendConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介政策失败")
		return
	}
	ratios, err := a.Store.ListRecommendRatios(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介政策失败")
		return
	}
	maxRatio := 0.0
	for _, r := range ratios {
		if r.Ratio > maxRatio {
			maxRatio = r.Ratio
		}
	}
	httpx.OK(c, 200, gin.H{"awards_cents": cfg.AwardsCents, "confirm_days": cfg.ConfirmDays, "max_ratio": maxRatio})
}

// myRecommendSystemURLs 可生成自定义链接的系统页面（后台配置的会员中心页面）。
func (a *App) myRecommendSystemURLs(c *gin.Context) {
	cfg, err := a.Store.GetRecommendConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介页面失败")
		return
	}
	urls := cfg.SystemURLs
	if urls == nil {
		urls = []string{}
	}
	httpx.OK(c, 200, gin.H{"system_urls": urls, "default_url": cfg.DefaultURL})
}

// myRecommendLinks 我的自定义推介链接。
func (a *App) myRecommendLinks(c *gin.Context) {
	p, _ := getPrincipal(c)
	links, err := a.Store.ListRecommendLinks(c, p.User.ID, a.recommendLinkBase(c))
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取自定义链接失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": links})
}

// createRecommendLink 生成自定义推介链接（系统页面 + 自定义后缀）。
func (a *App) createRecommendLink(c *gin.Context) {
	p, _ := getPrincipal(c)
	info, err := a.Store.RecommendPromoter(c, p.User.ID, a.recommendLinkBase(c))
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介信息失败")
		return
	}
	if info.UserID == 0 {
		httpx.Fail(c, 400, "RECOMMEND_NOT_OPEN", "请先开启推介计划")
		return
	}
	var in struct {
		SystemURL string `json:"system_url"`
		CustomURL string `json:"custom_url"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.SystemURL = strings.TrimSpace(in.SystemURL)
	in.CustomURL = strings.TrimSpace(in.CustomURL)
	if in.SystemURL == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择可推介的页面链接")
		return
	}
	if in.CustomURL == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请输入自定义后缀")
		return
	}
	if len([]rune(in.CustomURL)) > 64 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "自定义后缀不能超过 64 个字符")
		return
	}
	cfg, err := a.Store.GetRecommendConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介配置失败")
		return
	}
	allowed := false
	for _, u := range cfg.SystemURLs {
		if strings.EqualFold(strings.TrimSpace(u), in.SystemURL) {
			allowed = true
			break
		}
	}
	if !allowed {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择可推介的页面链接")
		return
	}
	link, err := a.Store.CreateRecommendLink(c, p.User.ID, in.SystemURL, in.CustomURL)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "生成自定义链接失败")
		return
	}
	httpx.OK(c, 200, gin.H{"link": link})
}

// deleteRecommendLink 删除自定义推介链接。
func (a *App) deleteRecommendLink(c *gin.Context) {
	p, _ := getPrincipal(c)
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "参数错误")
		return
	}
	ok, err := a.Store.DeleteRecommendLink(c, p.User.ID, id)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "删除链接失败")
		return
	}
	if !ok {
		httpx.Fail(c, 404, "RECOMMEND_LINK_NOT_FOUND", "链接不存在")
		return
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// recommendCopyLink 复制商品推介链接（自动带上当前用户推介码）。
func (a *App) recommendCopyLink(c *gin.Context) {
	p, _ := getPrincipal(c)
	productID := int64(parseIntDefault(c.Query("product_id"), 0))
	httpx.OK(c, 200, gin.H{"url": a.recommendProductURL(c, p.User.ID, productID)})
}

// myRecommendProducts 建议推介产品（主打推介）及每个产品的推介链接。
func (a *App) myRecommendProducts(c *gin.Context) {
	p, _ := getPrincipal(c)
	products, err := a.Store.ListRecommendProducts(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介产品失败")
		return
	}
	out := make([]gin.H, 0, len(products))
	for _, it := range products {
		out = append(out, gin.H{
			"product_id": it.ProductID,
			"name":       it.ProductName,
			"ratio":      it.Ratio,
			"url":        a.recommendProductURL(c, p.User.ID, it.ProductID),
		})
	}
	httpx.OK(c, 200, gin.H{"products": out})
}

// myRecommendAwards 我的推介奖励记录。
func (a *App) myRecommendAwards(c *gin.Context) {
	p, _ := getPrincipal(c)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	list, count, err := a.Store.ListPromoterRecommendAwards(c, p.User.ID, strings.TrimSpace(c.Query("status")), page, limit)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介记录失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list, "count": count, "page": page})
}

// myRecommendPolicy 完整推介政策（商品比例 + 提现规则）。
func (a *App) myRecommendPolicy(c *gin.Context) {
	cfg, err := a.Store.GetRecommendConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介政策失败")
		return
	}
	ratios, err := a.Store.ListRecommendRatios(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介政策失败")
		return
	}
	httpx.OK(c, 200, gin.H{
		"list": ratios,
		"config": gin.H{
			"awards_cents":          cfg.AwardsCents,
			"confirm_days":          cfg.ConfirmDays,
			"withdraw_min_cents":    cfg.WithdrawMinCents,
			"withdraw_handling_fee": cfg.WithdrawHandlingFee,
		},
	})
}

// myRecommendWithdrawals 我的提现记录。
func (a *App) myRecommendWithdrawals(c *gin.Context) {
	p, _ := getPrincipal(c)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	list, count, err := a.Store.ListPromoterRecommendWithdrawals(c, p.User.ID, strings.TrimSpace(c.Query("status")), page, limit)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取提现记录失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list, "count": count, "page": page})
}

// applyRecommendWithdraw 申请提现（可提现金额 = 已确认奖励 - 已申请）。
func (a *App) applyRecommendWithdraw(c *gin.Context) {
	p, _ := getPrincipal(c)
	var in struct {
		AmountCents int64  `json:"amount_cents"`
		Method      string `json:"method"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	out, err := a.Store.ApplyRecommendWithdraw(c, p.User.ID, in.AmountCents, strings.TrimSpace(in.Method))
	switch {
	case errors.Is(err, store.ErrRecommendAmount):
		httpx.Fail(c, 400, "INVALID_REQUEST", "提现金额不合法")
		return
	case errors.Is(err, store.ErrRecommendWithdrawMin):
		httpx.Fail(c, 400, "RECOMMEND_WITHDRAW_MIN", "未达到最低提现金额")
		return
	case errors.Is(err, store.ErrRecommendNotPromoter):
		httpx.Fail(c, 400, "RECOMMEND_NOT_OPEN", "请先开启推介计划")
		return
	case errors.Is(err, store.ErrRecommendInsufficient):
		httpx.Fail(c, 400, "RECOMMEND_INSUFFICIENT", "可提现金额不足")
		return
	case err != nil:
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "申请提现失败")
		return
	}
	_ = a.Store.Audit(c, p.User.ID, "recommend.withdraw", "recommend_withdrawal", out.PublicID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"amount_cents": out.AmountCents})
	httpx.OK(c, 200, gin.H{"withdrawal": out})
}

// recommendNotifyAward 奖励入账后通知推介人（站内通知 + 邮件；模板 recommend_notice 可覆盖）。
func (a *App) recommendNotifyAward(ctx context.Context, promoterID, count int64) {
	if promoterID <= 0 || count <= 0 {
		return
	}
	_ = a.Store.InsertNotification(ctx, promoterID, "recommend", "推介奖励到账",
		fmt.Sprintf("您有 %d 笔新的推介奖励记录，确认后即可提现。", count), "/referral")
	email, err := a.Store.UserEmail(ctx, promoterID)
	if err != nil || strings.TrimSpace(email) == "" {
		return
	}
	go func() {
		mctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		if !a.mailConfigured(mctx) {
			return
		}
		vars := map[string]string{
			"count": strconv.FormatInt(count, 10),
			"url":   strings.TrimRight(a.Cfg.PublicBaseURL, "/") + "/referral",
		}
		subject, body := a.renderMail("recommend_notice", "推介奖励到账通知",
			"<p>您好！</p><p>您有 {count} 笔新的推介奖励记录，确认后即可在「推广返佣」申请提现。</p><p><a href=\"{url}\">前往查看</a></p>", vars)
		if err := a.deliverMail(mctx, email, subject, body); err != nil {
			log.Printf("recommend notify mail to %s failed: %v", email, err)
		}
	}()
}

// ---- 后台：奖励记录 ----

// adminListRecommendAwards 奖励记录列表（推介人 / 被推介人 / 商品 / 状态 / 关键字）。
func (a *App) adminListRecommendAwards(c *gin.Context) {
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	f := store.RecommendAwardFilter{
		PromoterID: int64(parseIntDefault(c.Query("promoter_id"), 0)),
		ClientID:   int64(parseIntDefault(c.Query("client_id"), 0)),
		ProductID:  int64(parseIntDefault(c.Query("product_id"), 0)),
		Status:     strings.TrimSpace(c.Query("status")),
		Query:      strings.TrimSpace(c.Query("query")),
		Page:       page,
		Limit:      limit,
		OrderBy:    strings.TrimSpace(c.Query("orderby")),
		Sort:       strings.TrimSpace(c.Query("sort")),
	}
	list, count, err := a.Store.ListAdminRecommendAwards(c, f)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介奖励失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list, "count": count, "page": page})
}

// adminSetRecommendAwardAmount 修改奖励金额（分）。
func (a *App) adminSetRecommendAwardAmount(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "参数错误")
		return
	}
	var in struct {
		AwardsAmountCents int64 `json:"awards_amount_cents"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	ok, err := a.Store.AdminSetRecommendAwardAmount(c, id, in.AwardsAmountCents)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "修改奖励金额失败")
		return
	}
	if !ok {
		httpx.Fail(c, 404, "RECOMMEND_NOT_FOUND", "奖励记录不存在")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "recommend.award_amount", "recommend_award", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"awards_amount_cents": in.AwardsAmountCents})
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminRecommendAwardOp 奖励记录状态操作（confirm / frozen / unfrozen / invalid）。
func (a *App) adminRecommendAwardOp(c *gin.Context, op string) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "参数错误")
		return
	}
	reason := ""
	if op == "invalid" {
		var in struct {
			InvalidReason string `json:"invalid_reason"`
		}
		_ = c.ShouldBindJSON(&in)
		reason = strings.TrimSpace(in.InvalidReason)
		if reason == "" {
			httpx.Fail(c, 400, "INVALID_REQUEST", "请选择或填写无效原因")
			return
		}
		if len([]rune(reason)) > 500 {
			httpx.Fail(c, 400, "INVALID_REQUEST", "无效原因不能超过 500 个字符")
			return
		}
	}
	ok, err := a.Store.AdminRecommendAwardOp(c, id, op, reason)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "操作失败")
		return
	}
	if !ok {
		httpx.Fail(c, 404, "RECOMMEND_NOT_FOUND", "记录不存在或当前状态不允许此操作")
		return
	}
	if p, ok := getPrincipal(c); ok {
		var auditDetail map[string]any
		if reason != "" {
			auditDetail = map[string]any{"has_reason": true}
		}
		_ = a.Store.Audit(c, p.User.ID, "recommend.award_"+op, "recommend_award", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, auditDetail)
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminConfirmRecommendAward(c *gin.Context) { a.adminRecommendAwardOp(c, "confirm") }
func (a *App) adminFreezeRecommendAward(c *gin.Context)  { a.adminRecommendAwardOp(c, "frozen") }
func (a *App) adminUnfreezeRecommendAward(c *gin.Context) {
	a.adminRecommendAwardOp(c, "unfrozen")
}
func (a *App) adminInvalidRecommendAward(c *gin.Context) { a.adminRecommendAwardOp(c, "invalid") }

// adminDeleteRecommendAward 删除奖励记录。
func (a *App) adminDeleteRecommendAward(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "参数错误")
		return
	}
	ok, err := a.Store.AdminDeleteRecommendAward(c, id)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "删除失败")
		return
	}
	if !ok {
		httpx.Fail(c, 404, "RECOMMEND_NOT_FOUND", "奖励记录不存在")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "recommend.award_delete", "recommend_award", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 后台：预设无效回复 ----

func (a *App) adminListRecommendPrereplies(c *gin.Context) {
	list, err := a.Store.ListRecommendPrereplies(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取预设回复失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list})
}

func (a *App) adminCreateRecommendPrereply(c *gin.Context) {
	var in struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	content := strings.TrimSpace(in.Content)
	if content == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写回复内容")
		return
	}
	if len([]rune(content)) > 500 {
		httpx.Fail(c, 400, "INVALID_REQUEST", "回复内容不能超过 500 个字符")
		return
	}
	out, err := a.Store.CreateRecommendPrereply(c, content)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "保存预设回复失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "recommend.prereply_create", "recommend_prereply", strconv.FormatInt(out.ID, 10), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	}
	httpx.OK(c, 200, gin.H{"prereply": out})
}

func (a *App) adminUpdateRecommendPrereply(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "参数错误")
		return
	}
	var in struct {
		Content string `json:"content"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	content := strings.TrimSpace(in.Content)
	if content == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写回复内容")
		return
	}
	ok, err := a.Store.UpdateRecommendPrereply(c, id, content)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "保存预设回复失败")
		return
	}
	if !ok {
		httpx.Fail(c, 404, "RECOMMEND_NOT_FOUND", "预设回复不存在或内置项不可修改")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "recommend.prereply_update", "recommend_prereply", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

func (a *App) adminDeleteRecommendPrereply(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "参数错误")
		return
	}
	ok, err := a.Store.DeleteRecommendPrereply(c, id)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "删除预设回复失败")
		return
	}
	if !ok {
		httpx.Fail(c, 404, "RECOMMEND_NOT_FOUND", "预设回复不存在或内置项不可删除")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "recommend.prereply_delete", "recommend_prereply", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, nil)
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 后台：推介配置 ----

// adminGetRecommendConfig 推介配置（标量配置 + 商品比例 + 主打推介产品）。
func (a *App) adminGetRecommendConfig(c *gin.Context) {
	cfg, err := a.Store.GetRecommendConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介配置失败")
		return
	}
	ratios, err := a.Store.ListRecommendRatios(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介配置失败")
		return
	}
	products, err := a.Store.ListRecommendProducts(c)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取推介配置失败")
		return
	}
	httpx.OK(c, 200, gin.H{"config": cfg, "ratios": ratios, "products": products})
}

// adminSaveRecommendConfig 保存推介配置（整体覆盖商品比例与主打推介产品）。
func (a *App) adminSaveRecommendConfig(c *gin.Context) {
	var in struct {
		store.RecommendConfig
		Ratios   []store.RecommendRatio `json:"ratios"`
		Products []int64                `json:"products"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SaveRecommendConfig(c, in.RecommendConfig); err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "保存推介配置失败")
		return
	}
	if err := a.Store.ReplaceRecommendRatios(c, in.Ratios); err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "保存商品奖励比例失败")
		return
	}
	if err := a.Store.ReplaceRecommendProducts(c, in.Products); err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "保存主打推介产品失败")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "recommend.config_save", "recommend", "config", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, in.RecommendConfig)
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}

// ---- 后台：提现审核 ----

func (a *App) adminListRecommendWithdrawals(c *gin.Context) {
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit < 1 || limit > 100 {
		limit = 20
	}
	list, count, err := a.Store.ListAdminRecommendWithdrawals(c, strings.TrimSpace(c.Query("status")), page, limit)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "读取提现记录失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": list, "count": count, "page": page})
}

// adminSetRecommendWithdrawal 提现审核：1 通过（待打款）/ 2 驳回 / 3 已打款。
func (a *App) adminSetRecommendWithdrawal(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "参数错误")
		return
	}
	var in struct {
		Status int    `json:"status"`
		Reason string `json:"reason"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	reason := strings.TrimSpace(in.Reason)
	if in.Status == 2 && reason == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请填写驳回原因")
		return
	}
	ok, err := a.Store.AdminSetRecommendWithdrawal(c, id, in.Status, reason)
	if err != nil {
		httpx.Fail(c, 500, "RECOMMEND_FAILED", "操作失败")
		return
	}
	if !ok {
		httpx.Fail(c, 404, "RECOMMEND_NOT_FOUND", "提现记录不存在或状态已流转")
		return
	}
	if p, ok := getPrincipal(c); ok {
		_ = a.Store.Audit(c, p.User.ID, "recommend.withdrawal_review", "recommend_withdrawal", c.Param("id"), c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"status": in.Status})
	}
	httpx.OK(c, 200, gin.H{"ok": true})
}
