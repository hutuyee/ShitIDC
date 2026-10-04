package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 管理端统一搜索：后台不该要求管理员手抄 UUID。
//
// 一个入口查四类对象（用户 / 商品 / 服务 / 订单），返回统一的 {kind,id,label,sub} 结构，
// 前端弹窗用它渲染候选列表。用户既能按邮箱也能按 UID 搜，商品既能按名字也能按 UUID 搜。
func (a *App) adminSearch(c *gin.Context) {
	q := strings.TrimSpace(c.Query("q"))
	if len(q) < 1 {
		httpx.OK(c, 200, []any{})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(c.Query("kind")))
	limit := parseIntDefault(c.Query("limit"), 20)
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	results, err := a.Store.AdminSearch(c, q, kind, limit)
	if err != nil {
		httpx.Fail(c, 500, "SEARCH_FAILED", "搜索失败")
		return
	}
	httpx.OK(c, 200, results)
}

// adminUserDetail 一次返回一个用户的全部关键信息，供「点开用户」抽屉使用。
//
// 管理员排查问题时要看的东西天然是跨表的（余额 + 机器 + 订单 + 授信），
// 让前端发四五个请求既慢又要处理部分失败，不如后端一次给全。
func (a *App) adminUserDetail(c *gin.Context) {
	detail, err := a.Store.UserDetailAdmin(c, c.Param("id"))
	if errors.Is(err, store.ErrNotFound) {
		httpx.Fail(c, 404, "USER_NOT_FOUND", "用户不存在")
		return
	}
	if err != nil {
		httpx.Fail(c, 500, "USER_DETAIL_FAILED", "读取用户详情失败")
		return
	}
	httpx.OK(c, 200, detail)
}
