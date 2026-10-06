package api

import (
	"github.com/gin-gonic/gin"

	"github.com/hutuyee/ShitIDC/internal/httpx"
)

// 到期账单处理（对齐魔方主程序附属插件 expired_auto_delete_bill）。
//
// 设置只有一个字段：产品到期（终止）后其未支付续费账单的处理方式——无 / 直接删除 /
// 标记取消；记录页是「账单处理记录」。站内账目不物理删除：删除与取消的最终账面
// 状态都是 void（作废），选的动作记进日志；未配置时钩子为空操作，默认行为不变。

// adminGetExpiredBillAction 读取到期账单处理方式。
func (a *App) adminGetExpiredBillAction(c *gin.Context) {
	cfg, err := a.Store.GetExpiredBillConfig(c)
	if err != nil {
		httpx.Fail(c, 500, "EXPIRED_BILL_CONFIG_FAILED", "读取配置失败")
		return
	}
	httpx.OK(c, 200, cfg)
}

// adminSaveExpiredBillAction 保存到期账单处理方式。
func (a *App) adminSaveExpiredBillAction(c *gin.Context) {
	operator, ok := getPrincipal(c)
	if !ok {
		httpx.Fail(c, 401, "UNAUTHORIZED", "未登录")
		return
	}
	var in struct {
		Action string `json:"expired_bill_action"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		httpx.Fail(c, 400, "INVALID_REQUEST", "请求格式错误")
		return
	}
	if err := a.Store.SaveExpiredBillAction(c, in.Action); err != nil {
		httpx.Fail(c, 400, "EXPIRED_BILL_ACTION_INVALID", "处理方式只能是：无 / delete（直接删除）/ cancel（标记取消）")
		return
	}
	_ = a.Store.Audit(c, operator.User.ID, "expired_bill.config", "system", "expired_auto_delete_bill", c.GetString("request_id"), clientIP(c), c.Request.UserAgent(), nil, map[string]any{"action": in.Action})
	httpx.OK(c, 200, gin.H{"ok": true})
}

// adminListExpiredBillLogs 到期账单处理记录；关键词匹配账单号 / 产品 / IP / 用户邮箱。
func (a *App) adminListExpiredBillLogs(c *gin.Context) {
	limit := parseIntDefault(c.Query("limit"), 100)
	page := parseIntDefault(c.Query("page"), 1)
	if page < 1 {
		page = 1
	}
	items, total, err := a.Store.ListExpiredBillLogs(c, c.Query("keyword"), limit, (page-1)*limit)
	if err != nil {
		httpx.Fail(c, 500, "EXPIRED_BILL_LOGS_FAILED", "读取处理记录失败")
		return
	}
	httpx.OK(c, 200, gin.H{"list": items, "count": total, "page": page})
}
