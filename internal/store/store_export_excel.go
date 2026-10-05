package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// ---- 导出中心（对齐魔方附属插件 export_excel）----
//
// 参考插件（config/config.php）预置两个列表：
//
//	billPay     账单列表（已支付）：主机名称 / 主ip / 业务经理 / 付款方式 /
//	            账单编号 / 产品名称 / 客户名称 / 账单金额 / 在线支付金额 /
//	            余额 / 收款时间
//	achievement 我的业绩：同一组字段，按业务经理过滤
//
// ShitIDC 没有「业务经理」，业绩口径改用推广人（referral_commissions）；
// 账单不按主机分行：一单一行，主机名 / 主 IP 取该订单开通的第一个服务。

// ExportColumn describes one selectable column of a dataset.
type ExportColumn struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Money bool   `json:"money"` // 金额（分）：导出时转元并按数字单元格写出
	Time  bool   `json:"time"`
}

// ExportDataset is one built-in export list.
type ExportDataset struct {
	Key     string         `json:"key"`
	Name    string         `json:"name"`
	Columns []ExportColumn `json:"columns"`
}

// ExportDatasets returns the built-in catalog in display order.
func ExportDatasets() []ExportDataset {
	return []ExportDataset{
		{
			Key:  "bill_pay",
			Name: "账单列表（已支付）",
			Columns: []ExportColumn{
				{Key: "bill_num", Label: "账单编号"},
				{Key: "g_name", Label: "客户名称"},
				{Key: "pd_name", Label: "产品名称"},
				{Key: "croom", Label: "主机/服务"},
				{Key: "cip", Label: "主ip"},
				{Key: "paytype", Label: "付款方式"},
				{Key: "amount", Label: "账单金额(在线支付+余额)", Money: true},
				{Key: "stream", Label: "在线支付金额", Money: true},
				{Key: "balance", Label: "余额", Money: true},
				{Key: "mount_at", Label: "收款时间", Time: true},
			},
		},
		{
			Key:  "achievement",
			Name: "我的业绩（推广佣金）",
			Columns: []ExportColumn{
				{Key: "referrer", Label: "推广人"},
				{Key: "referee", Label: "客户名称"},
				{Key: "bill_num", Label: "账单编号"},
				{Key: "amount", Label: "账单金额", Money: true},
				{Key: "commission", Label: "佣金金额", Money: true},
				{Key: "mount_at", Label: "结算时间", Time: true},
			},
		},
	}
}

// ExportDatasetByKey resolves one dataset of the catalog.
func ExportDatasetByKey(key string) (ExportDataset, bool) {
	for _, d := range ExportDatasets() {
		if d.Key == key {
			return d, true
		}
	}
	return ExportDataset{}, false
}

// ValidateExportColumns checks a dataset/column selection and drops
// duplicates. At least one column must remain.
func ValidateExportColumns(dataset string, columns []string) ([]string, error) {
	d, ok := ExportDatasetByKey(strings.TrimSpace(dataset))
	if !ok {
		return nil, fmt.Errorf("未知导出列表 %q", dataset)
	}
	valid := map[string]bool{}
	for _, c := range d.Columns {
		valid[c.Key] = true
	}
	out := make([]string, 0, len(columns))
	seen := map[string]bool{}
	for _, c := range columns {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		if !valid[c] {
			return nil, fmt.Errorf("导出列表 %s 不支持字段 %q", dataset, c)
		}
		seen[c] = true
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, errors.New("至少选择一个导出字段")
	}
	return out, nil
}

// ExportConfig is a saved export list (custom_name + dataset + columns).
type ExportConfig struct {
	ID         int64     `json:"id"`
	CustomName string    `json:"custom_name"`
	Dataset    string    `json:"dataset"`
	Columns    []string  `json:"columns"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// ListExportConfigs returns all saved export lists.
func (s *Store) ListExportConfigs(ctx context.Context) ([]ExportConfig, error) {
	rows, err := s.DB.Query(ctx, `SELECT id,custom_name,dataset,columns,created_at,updated_at FROM export_configs ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExportConfig{}
	for rows.Next() {
		var v ExportConfig
		if err := rows.Scan(&v.ID, &v.CustomName, &v.Dataset, &v.Columns, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetExportConfig loads one saved export list.
func (s *Store) GetExportConfig(ctx context.Context, id int64) (ExportConfig, error) {
	var v ExportConfig
	err := s.DB.QueryRow(ctx, `SELECT id,custom_name,dataset,columns,created_at,updated_at FROM export_configs WHERE id=$1`, id).
		Scan(&v.ID, &v.CustomName, &v.Dataset, &v.Columns, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExportConfig{}, ErrNotFound
	}
	return v, err
}

// CreateExportConfig saves a new export list.
func (s *Store) CreateExportConfig(ctx context.Context, customName, dataset string, columns []string) (ExportConfig, error) {
	var v ExportConfig
	err := s.DB.QueryRow(ctx, `INSERT INTO export_configs(custom_name,dataset,columns) VALUES($1,$2,$3)
RETURNING id,custom_name,dataset,columns,created_at,updated_at`, customName, dataset, columns).
		Scan(&v.ID, &v.CustomName, &v.Dataset, &v.Columns, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

// UpdateExportConfig rewrites one export list.
func (s *Store) UpdateExportConfig(ctx context.Context, id int64, customName, dataset string, columns []string) (ExportConfig, error) {
	var v ExportConfig
	err := s.DB.QueryRow(ctx, `UPDATE export_configs SET custom_name=$2,dataset=$3,columns=$4,updated_at=now() WHERE id=$1
RETURNING id,custom_name,dataset,columns,created_at,updated_at`, id, customName, dataset, columns).
		Scan(&v.ID, &v.CustomName, &v.Dataset, &v.Columns, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ExportConfig{}, ErrNotFound
	}
	return v, err
}

// DeleteExportConfig removes one export list.
func (s *Store) DeleteExportConfig(ctx context.Context, id int64) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM export_configs WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ExportDatasetRows loads the rows behind one dataset. from/to bound the
// money-received timestamp (nil = unbounded, to is exclusive); limit caps the
// sheet size. Values stay typed (cents, time.Time, string) — the API formats
// them per column metadata.
func (s *Store) ExportDatasetRows(ctx context.Context, dataset string, from, to *time.Time, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 20000 {
		limit = 5000
	}
	switch dataset {
	case "bill_pay":
		return s.exportBillRows(ctx, from, to, limit)
	case "achievement":
		return s.exportCommissionRows(ctx, from, to, limit)
	default:
		return nil, fmt.Errorf("未知导出列表 %q", dataset)
	}
}

// exportBillRows: 已支付订单一行，付款方式/在线支付金额来自 payments，
// 余额 = 账单金额 - 在线支付；主机/服务与主 IP 取订单开通的第一个服务。
func (s *Store) exportBillRows(ctx context.Context, from, to *time.Time, limit int) ([]map[string]any, error) {
	rows, err := s.DB.Query(ctx, `SELECT o.public_id::text,u.email,o.total_cents,o.paid_at,
coalesce((SELECT string_agg(DISTINCT oi.product_name,'、') FROM order_items oi WHERE oi.order_id=o.id),''),
coalesce((SELECT string_agg(DISTINCT sv.public_id::text,'、') FROM services sv WHERE sv.order_id=o.id),''),
coalesce((SELECT sv.provider_payload->>'main_ip' FROM services sv WHERE sv.order_id=o.id AND coalesce(sv.provider_payload->>'main_ip','')<>'' ORDER BY sv.id LIMIT 1),''),
coalesce((SELECT string_agg(DISTINCT p.method,'、') FROM payments p WHERE p.order_id=o.id AND p.status='completed'),''),
coalesce((SELECT sum(p.amount_cents) FROM payments p WHERE p.order_id=o.id AND p.status='completed'),0)
FROM orders o JOIN users u ON u.id=o.user_id
WHERE o.paid_at IS NOT NULL AND o.status IN ('paid','processing','completed')
AND ($1::timestamptz IS NULL OR o.paid_at>=$1) AND ($2::timestamptz IS NULL OR o.paid_at<$2)
ORDER BY o.paid_at DESC LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var billNum, email, products, services, mainIP, methods string
		var total, stream int64
		var paidAt *time.Time
		if err := rows.Scan(&billNum, &email, &total, &paidAt, &products, &services, &mainIP, &methods, &stream); err != nil {
			return nil, err
		}
		balance := total - stream
		if balance < 0 {
			balance = 0
		}
		out = append(out, map[string]any{
			"bill_num": billNum, "g_name": email, "pd_name": products,
			"croom": services, "cip": mainIP, "paytype": methods,
			"amount": total, "stream": stream, "balance": balance, "mount_at": paidAt,
		})
	}
	return out, rows.Err()
}

// exportCommissionRows: 推广佣金明细（业绩口径）。
func (s *Store) exportCommissionRows(ctx context.Context, from, to *time.Time, limit int) ([]map[string]any, error) {
	rows, err := s.DB.Query(ctx, `SELECT ru.email,cu.email,o.public_id::text,o.total_cents,rc.amount_cents,rc.created_at
FROM referral_commissions rc
JOIN users ru ON ru.id=rc.referrer_id
JOIN users cu ON cu.id=rc.referee_id
JOIN orders o ON o.id=rc.order_id
WHERE ($1::timestamptz IS NULL OR rc.created_at>=$1) AND ($2::timestamptz IS NULL OR rc.created_at<$2)
ORDER BY rc.id DESC LIMIT $3`, from, to, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var referrer, referee, billNum string
		var amount, commission int64
		var createdAt time.Time
		if err := rows.Scan(&referrer, &referee, &billNum, &amount, &commission, &createdAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"referrer": referrer, "referee": referee, "bill_num": billNum,
			"amount": amount, "commission": commission, "mount_at": createdAt,
		})
	}
	return out, rows.Err()
}
