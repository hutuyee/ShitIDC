package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// 管理端统一搜索。
//
// 设计要点：**绝不要求管理员手抄 UUID**。用户按邮箱、UID、UUID 都能命中；
// 商品按名称、UUID 命中；服务与订单按 UUID / UID / 订单号命中。
//
// 返回结构统一成 {kind,id,label,sub,extra}，前端一个弹窗组件就能渲染全部类型。

// SearchHit 是一条搜索候选。
type SearchHit struct {
	Kind  string `json:"kind"`  // user / product / service / order
	ID    string `json:"id"`    // 供提交用的标识（用户是 UUID）
	Label string `json:"label"` // 主标题
	Sub   string `json:"sub"`   // 副标题（便于区分同名对象）
	extra map[string]any
}

// AdminSearch 跨类型搜索。kind 为空时搜索全部类型。
func (s *Store) AdminSearch(ctx context.Context, query, kind string, limit int) ([]SearchHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return []SearchHit{}, nil
	}
	like := "%" + strings.ToLower(query) + "%"
	// 纯数字当作 UID 处理（用户/服务/订单都用自增 ID 作为 UID）。
	uid, uidErr := strconv.ParseInt(query, 10, 64)
	hasUID := uidErr == nil && uid > 0
	out := []SearchHit{}
	want := func(k string) bool { return kind == "" || kind == k }

	if want("user") {
		rows, err := s.DB.Query(ctx, `SELECT public_id::text,id,email,status,
  (SELECT balance_cents FROM wallet_accounts w WHERE w.user_id=u.id AND w.currency='CNY')
FROM users u
WHERE u.deleted_at IS NULL AND (
  lower(u.email) LIKE $1
  OR ($2::bigint IS NOT NULL AND u.id = $2::bigint)
  OR u.public_id::text = lower($3)
)
ORDER BY u.id LIMIT $4`, like, nullableInt64(uid, hasUID), query, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var publicID, email, status string
			var id int64
			var balance *int64
			if err := rows.Scan(&publicID, &id, &email, &status, &balance); err != nil {
				rows.Close()
				return nil, err
			}
			hit := SearchHit{
				Kind: "user", ID: publicID, Label: email,
				Sub:   fmt.Sprintf("UID %d · %s", id, statusText(status)),
				extra: map[string]any{"uid": id, "email": email, "status": status},
			}
			if balance != nil {
				hit.extra["balance_cents"] = *balance
			}
			out = append(out, hit)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	if want("product") {
		rows, err := s.DB.Query(ctx, `SELECT p.public_id::text,p.name,coalesce(pr.amount_cents,0),coalesce(pr.currency,'CNY'),p.active
FROM products p
LEFT JOIN product_prices pr ON pr.product_id=p.id AND pr.active=true
WHERE p.deleted_at IS NULL AND (lower(p.name) LIKE $1 OR p.public_id::text = lower($2))
ORDER BY p.active DESC, p.id LIMIT $3`, like, query, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var publicID, name, currency string
			var price int64
			var active bool
			if err := rows.Scan(&publicID, &name, &price, &currency, &active); err != nil {
				rows.Close()
				return nil, err
			}
			sub := fmt.Sprintf("%s %.2f", currency, float64(price)/100)
			if !active {
				sub += " · 已下架"
			}
			out = append(out, SearchHit{
				Kind: "product", ID: publicID, Label: name, Sub: sub,
				extra: map[string]any{"active": active, "price_cents": price, "currency": currency},
			})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	if want("service") {
		rows, err := s.DB.Query(ctx, `SELECT sv.public_id::text,sv.status,u.email,p.name,sv.expires_at
FROM services sv
JOIN users u ON u.id=sv.user_id
JOIN products p ON p.id=sv.product_id
WHERE sv.public_id::text = lower($1)
   OR ($2::bigint IS NOT NULL AND sv.id = $2::bigint)
   OR lower(u.email) LIKE $3
ORDER BY sv.id DESC LIMIT $4`, query, nullableInt64(uid, hasUID), like, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var publicID, status, email, productName string
			var expires *string
			if err := rows.Scan(&publicID, &status, &email, &productName, &expires); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, SearchHit{
				Kind: "service", ID: publicID, Label: productName + " · " + email,
				Sub:   fmt.Sprintf("%s · %s", statusText(status), firstNonEmptyPtr(expires)),
				extra: map[string]any{"status": status, "email": email, "product": productName},
			})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}

	if want("order") {
		rows, err := s.DB.Query(ctx, `SELECT o.public_id::text,o.status,o.total_cents,o.currency,u.email
FROM orders o JOIN users u ON u.id=o.user_id
WHERE o.public_id::text = lower($1)
   OR ($2::bigint IS NOT NULL AND o.id = $2::bigint)
   OR lower(u.email) LIKE $3
ORDER BY o.id DESC LIMIT $4`, query, nullableInt64(uid, hasUID), like, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var publicID, status, currency, email string
			var total int64
			if err := rows.Scan(&publicID, &status, &total, &currency, &email); err != nil {
				rows.Close()
				return nil, err
			}
			out = append(out, SearchHit{
				Kind: "order", ID: publicID, Label: fmt.Sprintf("%s %.2f", currency, float64(total)/100),
				Sub:   statusText(status) + " · " + email,
				extra: map[string]any{"status": status, "total_cents": total, "currency": currency, "email": email},
			})
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// nullableInt64 把 (值, 是否有效) 转成 any，无效时为 nil（让 SQL 里的 IS NOT NULL 判断成立）。
func nullableInt64(v int64, ok bool) any {
	if !ok {
		return nil
	}
	return v
}

// statusText 把状态码转成中文，便于后台直接阅读。
func statusText(status string) string {
	switch status {
	case "active":
		return "正常"
	case "pending":
		return "待开通"
	case "provisioning":
		return "开通中"
	case "suspended":
		return "已暂停"
	case "terminated":
		return "已终止"
	case "failed":
		return "开通失败"
	case "unpaid":
		return "未付款"
	case "paid":
		return "已付款"
	case "processing":
		return "处理中"
	case "cancelled":
		return "已取消"
	case "refunded":
		return "已退款"
	case "disabled":
		return "已禁用"
	}
	return status
}

// firstNonEmptyPtr 安全地取指针字符串，空值给占位符。
func firstNonEmptyPtr(v *string) string {
	if v == nil || strings.TrimSpace(*v) == "" {
		return "—"
	}
	return *v
}

var _ = errors.Is
var _ = pgx.ErrNoRows

// ---- 用户详情聚合 ----

// UserDetail 是后台「点开一个用户」需要的全部信息。
//
// 管理员排查问题时要看的东西天然跨表（余额 + 机器 + 订单 + 授信），
// 让前端发四五个请求既慢又要处理部分失败，所以后端一次给全。
type UserDetail struct {
	PublicID      string  `json:"id"`
	UID           int64   `json:"uid"`
	Email         string  `json:"email"`
	Status        string  `json:"status"`
	EmailVerified bool    `json:"email_verified"`
	Phone         string  `json:"phone,omitempty"`
	PhoneVerified bool    `json:"phone_verified"`
	GroupName     string  `json:"group_name,omitempty"`
	GroupDiscount int     `json:"group_discount"`
	CreatedAt     string  `json:"created_at"`
	LastLoginAt   *string `json:"last_login_at,omitempty"`
	// 钱包按币种列出，因为多币种下余额是分开的。
	Wallets []UserWallet `json:"wallets"`
	// 统计。
	OrderCount     int   `json:"order_count"`
	ServiceCount   int   `json:"service_count"`
	ActiveServices int   `json:"active_services"`
	PaidTotalCents int64 `json:"paid_total_cents"`
	// 授信（后付费）。
	Credit CreditAccount `json:"credit"`
	// 最近的机器与订单，抽屉里直接看得到，不用再跳页面。
	Services []UserDetailService `json:"services"`
	Orders   []UserDetailOrder   `json:"orders"`
}

// UserWallet 是某个币种的钱包余额。
type UserWallet struct {
	Currency     string `json:"currency"`
	BalanceCents int64  `json:"balance_cents"`
}

// UserDetailService 是用户的一台机器（抽屉里展示用）。
type UserDetailService struct {
	PublicID     string  `json:"id"`
	ProductName  string  `json:"product_name"`
	Status       string  `json:"status"`
	ProviderType string  `json:"provider_type"`
	ExpiresAt    *string `json:"expires_at,omitempty"`
	BillingCycle string  `json:"billing_cycle,omitempty"`
}

// UserDetailOrder 是用户的一笔订单。
type UserDetailOrder struct {
	PublicID   string `json:"id"`
	Status     string `json:"status"`
	Kind       string `json:"kind"`
	PayMethod  string `json:"pay_method"`
	TotalCents int64  `json:"total_cents"`
	Currency   string `json:"currency"`
	CreatedAt  string `json:"created_at"`
}

// UserDetailAdmin 按用户公开 ID（或 "uid:123" 这种写法）取聚合详情。
func (s *Store) UserDetailAdmin(ctx context.Context, idOrUID string) (UserDetail, error) {
	var d UserDetail
	var userID int64
	var groupName *string
	idOrUID = strings.TrimSpace(idOrUID)
	// 允许直接传纯数字 UID：后台链接与手输都方便。
	if uid, err := strconv.ParseInt(idOrUID, 10, 64); err == nil && uid > 0 {
		userID = uid
	}
	err := s.DB.QueryRow(ctx, `SELECT u.id,u.public_id::text,u.email,u.status,u.email_verified,u.phone,u.phone_verified,
  ug.name, coalesce(ug.discount_percent,0), u.created_at::text
FROM users u
LEFT JOIN user_groups ug ON ug.id=u.user_group_id
WHERE u.deleted_at IS NULL AND (u.public_id::text=$1 OR u.id=$2)`,
		idOrUID, userID).Scan(&userID, &d.PublicID, &d.Email, &d.Status, &d.EmailVerified,
		&d.Phone, &d.PhoneVerified, &groupName, &d.GroupDiscount, &d.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, err
	}
	d.UID = userID
	if groupName != nil {
		d.GroupName = *groupName
	}

	// 最近一次成功登录。
	var lastLogin *string
	_ = s.DB.QueryRow(ctx, `SELECT max(created_at)::text FROM login_attempts WHERE user_id=$1 AND success`, userID).Scan(&lastLogin)
	d.LastLoginAt = lastLogin

	// 钱包（按币种）。
	wrows, err := s.DB.Query(ctx, `SELECT currency,balance_cents FROM wallet_accounts WHERE user_id=$1 ORDER BY currency`, userID)
	if err != nil {
		return d, err
	}
	d.Wallets = []UserWallet{}
	for wrows.Next() {
		var w UserWallet
		if err := wrows.Scan(&w.Currency, &w.BalanceCents); err != nil {
			wrows.Close()
			return d, err
		}
		d.Wallets = append(d.Wallets, w)
	}
	wrows.Close()
	if err := wrows.Err(); err != nil {
		return d, err
	}

	// 统计：一笔查询算完订单数、机器数与累计已付金额。
	if err := s.DB.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM orders WHERE user_id=$1),
  (SELECT count(*) FROM services WHERE user_id=$1 AND status NOT IN ('terminated','failed')),
  (SELECT count(*) FROM services WHERE user_id=$1 AND status='active'),
  coalesce((SELECT sum(total_cents) FROM orders WHERE user_id=$1 AND status IN ('paid','processing')),0)`, userID).
		Scan(&d.OrderCount, &d.ServiceCount, &d.ActiveServices, &d.PaidTotalCents); err != nil {
		return d, err
	}

	// 授信：拿不到也不算错（用户可能从没开通过后付费）。
	if credit, err := s.CreditAccount(ctx, userID); err == nil {
		d.Credit = credit
	}

	// 最近的机器。
	srows, err := s.DB.Query(ctx, `SELECT sv.public_id::text,p.name,sv.status,sv.provider_type,sv.expires_at::text,
  coalesce(oi.billing_cycle,'')
FROM services sv
JOIN products p ON p.id=sv.product_id
LEFT JOIN order_items oi ON oi.id=sv.order_item_id
WHERE sv.user_id=$1 ORDER BY sv.id DESC LIMIT 20`, userID)
	if err != nil {
		return d, err
	}
	d.Services = []UserDetailService{}
	for srows.Next() {
		var v UserDetailService
		if err := srows.Scan(&v.PublicID, &v.ProductName, &v.Status, &v.ProviderType, &v.ExpiresAt, &v.BillingCycle); err != nil {
			srows.Close()
			return d, err
		}
		d.Services = append(d.Services, v)
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return d, err
	}

	// 最近的订单。
	rows, err := s.DB.Query(ctx, `SELECT public_id::text,status,kind,pay_method,total_cents,currency,created_at::text
FROM orders WHERE user_id=$1 ORDER BY id DESC LIMIT 20`, userID)
	if err != nil {
		return d, err
	}
	d.Orders = []UserDetailOrder{}
	for rows.Next() {
		var v UserDetailOrder
		if err := rows.Scan(&v.PublicID, &v.Status, &v.Kind, &v.PayMethod, &v.TotalCents, &v.Currency, &v.CreatedAt); err != nil {
			rows.Close()
			return d, err
		}
		d.Orders = append(d.Orders, v)
	}
	rows.Close()
	return d, rows.Err()
}
