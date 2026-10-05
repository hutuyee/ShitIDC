package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 对齐魔方附属插件 expired_ip_log（到期产品删除IP记录）：产品终止时留档
// IP 与开通时间。参考实现挂在 afterModuleTerminate 上，写 dedicatedip /
// assignedips / host_create_time(regdate) / uid / create_time；ShitIDC 的
// 对应来源是 services.provider_payload（开通成功时供应商返回的实例数据，
// 已记录 IP 的如 NOKVM 的 main_ip / assigned_ips）。

// ExpiredIPLog is one archived IP record.
type ExpiredIPLog struct {
	ID               int64      `json:"id"`
	ServiceID        string     `json:"service_id"`
	UserID           int64      `json:"user_uid"`
	UserEmail        string     `json:"user_email"`
	ProductName      string     `json:"product_name"`
	DedicatedIP      string     `json:"dedicated_ip"`
	AssignedIPs      string     `json:"assigned_ips"`
	ServiceCreatedAt *time.Time `json:"service_created_at"`
	CreatedAt        time.Time  `json:"created_at"`
}

// RecordExpiredIPLog snapshots a terminated service into expired_ip_logs.
// Called after the terminate transition has been finalized; a logging failure
// must not fail the termination itself (caller logs and continues).
func (s *Store) RecordExpiredIPLog(ctx context.Context, servicePublicID string) error {
	var userID int64
	var createdAt time.Time
	var productName string
	var payload []byte
	err := s.DB.QueryRow(ctx, `SELECT s.user_id,s.created_at,coalesce(oi.product_name,''),s.provider_payload
FROM services s LEFT JOIN order_items oi ON oi.id=s.order_item_id
WHERE s.public_id=$1`, servicePublicID).Scan(&userID, &createdAt, &productName, &payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	data := map[string]any{}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &data)
	}
	dedicated, assigned := expiredIPsFromPayload(data)
	_, err = s.DB.Exec(ctx, `INSERT INTO expired_ip_logs(service_id,user_id,product_name,dedicated_ip,assigned_ips,service_created_at)
VALUES($1,$2,$3,$4,$5,$6)`, servicePublicID, userID, productName, dedicated, assigned, createdAt)
	return err
}

// ListExpiredIPLogs returns archived IP records, newest first.
func (s *Store) ListExpiredIPLogs(ctx context.Context, query string, limit int) ([]ExpiredIPLog, error) {
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	rows, err := s.DB.Query(ctx, `SELECT l.id,l.service_id,l.user_id,u.email,l.product_name,l.dedicated_ip,l.assigned_ips,l.service_created_at,l.created_at
FROM expired_ip_logs l JOIN users u ON u.id=l.user_id
WHERE ($1='' OR l.service_id ILIKE '%'||$1||'%' OR l.dedicated_ip ILIKE '%'||$1||'%' OR l.assigned_ips ILIKE '%'||$1||'%' OR u.email ILIKE '%'||$1||'%' OR l.product_name ILIKE '%'||$1||'%')
ORDER BY l.id DESC LIMIT $2`, strings.TrimSpace(query), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExpiredIPLog{}
	for rows.Next() {
		var v ExpiredIPLog
		var serviceCreated *time.Time
		if err := rows.Scan(&v.ID, &v.ServiceID, &v.UserID, &v.UserEmail, &v.ProductName, &v.DedicatedIP, &v.AssignedIPs, &serviceCreated, &v.CreatedAt); err != nil {
			return nil, err
		}
		v.ServiceCreatedAt = serviceCreated
		out = append(out, v)
	}
	return out, rows.Err()
}

// expiredIPsFromPayload extracts the IP snapshot from provider instance data.
// Keys follow the providers that record IPs today (nokvm: main_ip /
// assigned_ips), with common aliases for custom / magiccube upstream payloads.
func expiredIPsFromPayload(data map[string]any) (dedicated, assigned string) {
	for _, key := range []string{"dedicatedip", "dedicated_ip", "main_ip", "mainip", "server_ip", "ip", "ip_address"} {
		if v := payloadString(data[key]); v != "" {
			dedicated = v
			break
		}
	}
	for _, key := range []string{"assignedips", "assigned_ips", "ips", "public_ips", "extra_ips"} {
		if v := payloadString(data[key]); v != "" {
			assigned = v
			break
		}
	}
	return dedicated, assigned
}

// payloadString renders a JSON value for a text column: strings pass through,
// arrays join with commas, numbers/booleans use their literal form.
func payloadString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(t)
	case []string:
		return strings.Join(t, ",")
	case []any:
		parts := make([]string, 0, len(t))
		for _, item := range t {
			if s := payloadString(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ",")
	case json.Number:
		return t.String()
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return ""
		}
		s := strings.TrimSpace(string(b))
		if s == "{}" || s == "[]" || s == "null" {
			return ""
		}
		return s
	}
}
