package events

// Info 是事件的中文名，供后台「值邮件通知管理员」等配置界面展示。
type Info struct {
	Name  string `json:"event"`
	Label string `json:"name_lang"`
}

// Catalog 返回可配置通知的核心事件清单（与上方 const 列表逐一对应）。
func Catalog() []Info {
	return []Info{
		{UserRegistered, "用户注册"},
		{UserLogin, "用户登录"},
		{UserPasswordReset, "密码重置"},
		{OrderCreated, "订单创建"},
		{OrderPaid, "订单支付"},
		{OrderCancelled, "订单取消"},
		{OrderRefunded, "订单退款"},
		{InvoicePaid, "账单支付"},
		{WalletRecharged, "余额充值"},
		{WalletAdjusted, "余额调整"},
		{PaymentFailed, "支付失败"},
		{ServiceCreated, "服务开通"},
		{ServiceFailed, "服务开通失败"},
		{ServiceUpdated, "服务变更"},
		{ServiceRenewed, "服务续费"},
		{ServiceSuspended, "服务暂停"},
		{ServiceUnsuspended, "服务恢复"},
		{ServiceTerminated, "服务删除"},
		{TicketCreated, "工单新建"},
		{TicketReplied, "工单回复"},
	}
}

// Label 返回事件的中文名；未收录时回退为事件名。
func Label(name string) string {
	for _, it := range Catalog() {
		if it.Name == name {
			return it.Label
		}
	}
	return name
}
