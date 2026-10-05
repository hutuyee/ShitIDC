package api

import (
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// 产品转移（对齐魔方 CBAP HostTransfer 插件）。
//
// 后台在「服务管理」或「用户详情 → 机器」里把产品转移到另一个用户名下；
// 同一订单的关联产品会一起迁移（订单不动），每次迁移留一条记录。

// adminListServiceTransfers 列出转移记录（关键词：商品 / 产品ID / 双方邮箱 / 备注）。
func (a *App) adminListServiceTransfers(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 100)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	items, total, err := a.Store.ListServiceTransferLogs(c, c.Query("keyword"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "SERVICE_TRANSFERS_FAILED", "读取转移记录失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items, "count": total, "page": page})
}

// adminTransferService 执行产品转移：service_id 为服务公开 ID，to_user 为 UID / UUID / 邮箱。
func (a *App) adminTransferService(c *gin.Context) {
	var in struct {
		ServiceID string `json:"service_id"`
		ToUser    string `json:"to_user"`
		Remark    string `json:"remark"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	in.ServiceID = strings.TrimSpace(in.ServiceID)
	in.ToUser = strings.TrimSpace(in.ToUser)
	if in.ServiceID == "" || in.ToUser == "" {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请选择要转移的产品和目标用户")
		return
	}
	target, err := a.Store.GetUserByIDOrPublicID(c, in.ToUser)
	if err != nil {
		httpx.Fail(c, 404, "SERVICE_TRANSFER_USER_NOT_FOUND", "目标用户不存在")
		return
	}
	if target.Status != "active" {
		httpx.Fail(c, 400, "SERVICE_TRANSFER_USER_INACTIVE", "目标用户不是正常状态")
		return
	}
	operator, ok := getPrincipal(c)
	if !ok {
		httpx.Fail(c, 401, "UNAUTHORIZED", "未登录")
		return
	}
	res, err := a.Store.TransferService(c, in.ServiceID, target.ID, operator.User.ID, in.Remark)
	switch {
	case errors.Is(err, store.ErrNotFound):
		httpx.Fail(c, 404, "SERVICE_TRANSFER_NOT_FOUND", "产品不存在或目标用户不可用")
		return
	case errors.Is(err, store.ErrTransferSelf):
		httpx.Fail(c, 400, "SERVICE_TRANSFER_SELF", "目标用户就是产品当前所有者")
		return
	case errors.Is(err, store.ErrTransferTerminated):
		httpx.Fail(c, 400, "SERVICE_TRANSFER_TERMINATED", "已删除的产品不能转移")
		return
	case err != nil:
		httpx.Fail(c, 500, "SERVICE_TRANSFER_FAILED", "转移失败")
		return
	}
	_ = a.Store.Audit(c, operator.User.ID, "service.transfer", "service", in.ServiceID, c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"to_uid": target.ID, "to_email": target.Email, "moved": res.Moved})
	httpx.OK(c, 200, gin.H{"ok": true, "moved": res.Moved, "from_uid": res.FromUserID, "to_uid": res.ToUserID})
}
