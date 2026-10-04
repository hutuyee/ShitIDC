// Package notify subscribes to the core event bus (第十一阶段 Hook 系统)
// and fans each committed event out to the in-app notification center and
// the WASM extension runtime. Nothing here can fail the business flow that
// emitted the event.
package notify

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/hutuyee/ShitIDC/internal/events"
	"github.com/hutuyee/ShitIDC/internal/extension"
	"github.com/hutuyee/ShitIDC/internal/store"
)

// Install wires the notification + extension dispatchers to the bus.
// Call once per process at startup (server and worker).
func Install(bus *events.Bus, st *store.Store, extHost *extension.Host) {
	bus.Subscribe(func(ctx context.Context, e events.Event) {
		if extHost != nil {
			payload, err := json.Marshal(e)
			if err == nil {
				extHost.Dispatch(ctx, e.Name, payload)
			}
		}
		userID := extractUserID(e)
		if userID <= 0 {
			return
		}
		n := notificationFor(e)
		if n == nil {
			return
		}
		if err := st.InsertNotification(ctx, userID, n.typ, n.title, n.body, n.link); err != nil {
			log.Printf("notify: insert notification for %s: %v", e.Name, err)
		}
	})
}

// extractUserID accepts the uid spellings used across emitters.
func extractUserID(e events.Event) int64 {
	for _, key := range []string{"uid", "user_uid"} {
		switch v := e.Data[key].(type) {
		case int64:
			if v > 0 {
				return v
			}
		case float64:
			if v > 0 {
				return int64(v)
			}
		case json.Number:
			var n int64
			if err := json.Unmarshal([]byte(v), &n); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

type notification struct {
	typ   string
	title string
	body  string
	link  string
}

// notificationFor maps committed events to user-visible notices.
func notificationFor(e events.Event) *notification {
	switch e.Name {
	case events.OrderCreated:
		return &notification{typ: "order", title: "订单已创建", body: "请在 24 小时内完成支付，超时订单将自动关闭。", link: "/orders"}
	case events.OrderPaid:
		kind, _ := e.Data["kind"].(string)
		if kind == "renewal" {
			return &notification{typ: "order", title: "续费支付成功", body: "到期时间已自动顺延一个周期。", link: "/services"}
		}
		return &notification{typ: "order", title: "支付成功", body: "服务已进入开通队列，开通完成后可在服务中心查看。", link: "/orders"}
	case events.OrderRefunded:
		return &notification{typ: "order", title: "订单已退款", body: "退款已按原路处理，请查收。", link: "/orders"}
	case events.InvoicePaid:
		return &notification{typ: "invoice", title: "账单已支付", link: "/invoices"}
	case events.WalletRecharged:
		return &notification{typ: "wallet", title: "充值到账", body: "余额已更新。", link: "/wallet"}
	case events.WalletAdjusted:
		return &notification{typ: "wallet", title: "余额已被管理员调整", body: str(e.Data["reason"]), link: "/wallet"}
	case events.ServiceCreated:
		return &notification{typ: "service", title: "服务已开通", body: "实例信息可在服务中心查看。", link: "/services"}
	case events.ServiceFailed:
		return &notification{typ: "service", title: "服务开通失败", body: "工作人员会尽快处理，或请联系客服。", link: "/services"}
	case events.ServiceSuspended:
		return &notification{typ: "service", title: "服务已暂停", body: "如需恢复请续费或联系客服。", link: "/services"}
	case events.ServiceUnsuspended:
		return &notification{typ: "service", title: "服务已恢复", link: "/services"}
	case events.ServiceTerminated:
		return &notification{typ: "service", title: "服务已删除", link: "/services"}
	case events.ServiceRenewed:
		return &notification{typ: "service", title: "服务续费成功", body: "到期时间已顺延。", link: "/services"}
	case events.TicketReplied:
		return &notification{typ: "ticket", title: "工单有新回复", link: "/tickets/" + str(e.Data["ticket_id"])}
	}
	return nil
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
