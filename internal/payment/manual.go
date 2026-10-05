package payment

import (
	"context"
	"errors"
)

// ManualGateway 对齐魔方财务 user_custom（线下支付）插件：不下单、不跳转，
// 结账时把后台配置的收款说明（HTML）返回给前端展示，管理员确认线下收款后
// 再在后台「确认收款」完结订单。
type ManualGateway struct{}

// Method 返回通道标识。
func (ManualGateway) Method() string { return "manual" }

// PayURL 线下支付没有收银台地址；结账链路会在调用前短路。
func (ManualGateway) PayURL(_ context.Context, _ ProviderConfig, _ Prepared) (string, error) {
	return "", errors.New("线下支付不支持跳转收银台")
}

// VerifyNotify 线下支付没有异步回调。
func (ManualGateway) VerifyNotify(_ ProviderConfig, _ NotifyInput) NotifyResult {
	return NotifyResult{OK: false, Err: errors.New("线下支付不支持异步回调")}
}

func init() { Register(ManualGateway{}) }
