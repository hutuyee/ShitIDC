package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
)

// 按量 / 超量计费（魔方 overages_* 的 Go 实现）。
//
// 模型：每个计量资源有一份「包含额度」，超出部分按单价计费。
//   - 磁盘按 MB 统计、按 GB 计价（不足 1GB 向上取整，避免出现 0 元账单）
//   - 流量按 GB 统计、按 GB 计价（保留两位小数，不向上取整——流量是连续量）
//   - billed_* 是「已出账水位」，只对水位之上的新增用量收费，重复出账不会多收钱
//   - limit_* 是硬上限，超过就暂停服务（0 = 不限制）

// MeteredPlan 是一个商品的计量配置。
type MeteredPlan struct {
	Enabled           bool
	DiskIncludedMB    int64
	DiskLimitMB       int64
	DiskPricePerGB    int64
	BWIncludedGB      int64
	BWLimitGB         int64
	BWPricePerGBCents int64
	Billing           string // cycle_end | immediate
}

// OverageLine 是一条超量计费明细。
type OverageLine struct {
	Resource  string `json:"resource"` // disk | bandwidth
	Quantity  int64  `json:"quantity"`
	UnitPrice int64  `json:"unit_price_cents"`
	Amount    int64  `json:"amount_cents"`
	OverLimit bool   `json:"over_limit"`
}

// OverageQuote 是一次超量计算的完整结果。
type OverageQuote struct {
	Lines         []OverageLine
	TotalCents    int64
	OverHardLimit bool
}

// ComputeOverage 计算「相对已出账水位」的新增超量费用。纯函数，便于测试。
//
//	usageDiskMB / usageBWG    当前用量
//	billedDiskMB / billedBWG  已出账水位（这些已经在之前的账单里收过钱了）
func ComputeOverage(plan MeteredPlan, usageDiskMB, billedDiskMB, usageBWG, billedBWG int64) OverageQuote {
	quote := OverageQuote{Lines: []OverageLine{}}
	if !plan.Enabled {
		return quote
	}

	// ---- 磁盘：MB 统计，GB 计价，向上取整 ----
	if plan.DiskPricePerGB > 0 {
		billableMB := usageDiskMB - plan.DiskIncludedMB
		alreadyMB := billedDiskMB - plan.DiskIncludedMB
		if alreadyMB < 0 {
			alreadyMB = 0
		}
		if billableMB > alreadyMB {
			newMB := billableMB - alreadyMB
			gb := int64(math.Ceil(float64(newMB) / 1024.0))
			if gb > 0 {
				quote.Lines = append(quote.Lines, OverageLine{
					Resource:  "disk",
					Quantity:  gb,
					UnitPrice: plan.DiskPricePerGB,
					Amount:    gb * plan.DiskPricePerGB,
				})
			}
		}
	}
	if plan.DiskLimitMB > 0 && usageDiskMB > plan.DiskLimitMB {
		quote.OverHardLimit = true
	}

	// ---- 流量：整数 GB 统计、按 GB 计价 ----
	// usage_bw_gb 是整数 GB（Provider 上报的流量本身就按 GB 取整），所以这里不做
	// 小数换算——用整数运算可以彻底避免浮点误差把账目算歪。
	if plan.BWPricePerGBCents > 0 {
		billableGB := usageBWG - plan.BWIncludedGB
		alreadyGB := billedBWG - plan.BWIncludedGB
		if alreadyGB < 0 {
			alreadyGB = 0
		}
		if billableGB > alreadyGB {
			newGB := billableGB - alreadyGB
			amount := newGB * plan.BWPricePerGBCents
			if amount > 0 {
				quote.Lines = append(quote.Lines, OverageLine{
					Resource:  "bandwidth",
					Quantity:  newGB,
					UnitPrice: plan.BWPricePerGBCents,
					Amount:    amount,
				})
			}
		}
	}
	if plan.BWLimitGB > 0 && usageBWG > plan.BWLimitGB {
		quote.OverHardLimit = true
	}

	for _, line := range quote.Lines {
		quote.TotalCents += line.Amount
	}
	return quote
}

// ---- 用量上报 ----

// ReportUsage 记录一次用量回报。用量在一个周期内只增不减，上报更小的值会被忽略，
// 避免 Provider 数值抖动导致少收费。
func (s *Store) ReportUsage(ctx context.Context, servicePublicID string, diskMB, bwGB int64) error {
	if diskMB < 0 || bwGB < 0 {
		return fmt.Errorf("用量不能为负数")
	}
	tag, err := s.DB.Exec(ctx, `UPDATE services SET
usage_disk_mb=GREATEST(usage_disk_mb,$2),
usage_bw_gb=GREATEST(usage_bw_gb,$3),
usage_reported_at=now(),
updated_at=now()
WHERE public_id=$1`, servicePublicID, diskMB, bwGB)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ResetUsageCycle 在续费或新周期开始时清零用量与已出账水位。
func (s *Store) ResetUsageCycle(ctx context.Context, servicePublicID string) error {
	_, err := s.DB.Exec(ctx, `UPDATE services SET usage_disk_mb=0,usage_bw_gb=0,billed_disk_mb=0,billed_bw_gb=0,overage_suspended_at=NULL,updated_at=now() WHERE public_id=$1`, servicePublicID)
	return err
}

// LoadMeteredPlan 读出某个服务对应商品的计量配置。
func (s *Store) LoadMeteredPlan(ctx context.Context, servicePublicID string) (MeteredPlan, error) {
	var plan MeteredPlan
	err := s.DB.QueryRow(ctx, `SELECT p.overage_enabled,p.disk_included_mb,p.disk_limit_mb,p.disk_price_cents_per_gb,p.bw_included_gb,p.bw_limit_gb,p.bw_price_cents_per_gb,p.overage_billing
FROM products p JOIN services s ON s.product_id=p.id WHERE s.public_id=$1`, servicePublicID).Scan(
		&plan.Enabled, &plan.DiskIncludedMB, &plan.DiskLimitMB, &plan.DiskPricePerGB,
		&plan.BWIncludedGB, &plan.BWLimitGB, &plan.BWPricePerGBCents, &plan.Billing)
	if errors.Is(err, pgx.ErrNoRows) {
		return plan, ErrNotFound
	}
	return plan, err
}

// MeteredUsage 是一个服务的当前用量与已出账水位。
type MeteredUsage struct {
	ServiceID    int64
	UserID       int64
	Currency     string
	UsageDiskMB  int64
	BilledDiskMB int64
	UsageBWG     int64
	BilledBWG    int64
	Plan         MeteredPlan
}

func (s *Store) loadMeteredUsage(ctx context.Context, tx pgx.Tx, servicePublicID string) (MeteredUsage, error) {
	var u MeteredUsage
	err := tx.QueryRow(ctx, `SELECT s.id,s.user_id,coalesce(o.currency,'CNY'),s.usage_disk_mb,s.billed_disk_mb,s.usage_bw_gb,s.billed_bw_gb,
p.overage_enabled,p.disk_included_mb,p.disk_limit_mb,p.disk_price_cents_per_gb,
p.bw_included_gb,p.bw_limit_gb,p.bw_price_cents_per_gb,p.overage_billing
FROM services s
JOIN products p ON p.id=s.product_id
JOIN orders o ON o.id=s.order_id
WHERE s.public_id=$1`, servicePublicID).Scan(
		&u.ServiceID, &u.UserID, &u.Currency, &u.UsageDiskMB, &u.BilledDiskMB, &u.UsageBWG, &u.BilledBWG,
		&u.Plan.Enabled, &u.Plan.DiskIncludedMB, &u.Plan.DiskLimitMB, &u.Plan.DiskPricePerGB,
		&u.Plan.BWIncludedGB, &u.Plan.BWLimitGB, &u.Plan.BWPricePerGBCents, &u.Plan.Billing)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// ---- 出账 ----

// OverageResult 描述一次出账的结果。
type OverageResult struct {
	Lines      []OverageLine `json:"lines"`
	TotalCents int64         `json:"total_cents"`
	OrderID    string        `json:"order_id,omitempty"`
	Suspended  bool          `json:"over_limit"`
	Message    string        `json:"message"`
}

// SettleOverage 结算一个服务的超量费用并推进已出账水位。
//
// 只对「水位之上」的新增用量计费，所以重复调用是安全的：没有新增用量时返回 0 元。
// 超过硬上限时把服务标记为待暂停（overage_suspended_at），由调度器真正暂停，
// 这样计费与生命周期解耦，不会因为一次出账失败就影响服务状态。
func (s *Store) SettleOverage(ctx context.Context, servicePublicID string) (OverageResult, error) {
	tx, err := s.DB.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return OverageResult{}, err
	}
	defer tx.Rollback(ctx)
	u, err := s.loadMeteredUsage(ctx, tx, servicePublicID)
	if err != nil {
		return OverageResult{}, err
	}
	res := OverageResult{Lines: []OverageLine{}}
	if !u.Plan.Enabled {
		return res, nil
	}
	quote := ComputeOverage(u.Plan, u.UsageDiskMB, u.BilledDiskMB, u.UsageBWG, u.BilledBWG)
	res.Lines = quote.Lines
	res.TotalCents = quote.TotalCents
	res.Suspended = quote.OverHardLimit

	for _, line := range quote.Lines {
		if _, err := tx.Exec(ctx, `INSERT INTO overage_charges(service_id,user_id,resource,quantity,unit_price_cents,amount_cents,currency,period_start,period_end)
VALUES($1,$2,$3,$4,$5,$6,$7,now()-interval '1 month',now())`,
			u.ServiceID, u.UserID, line.Resource, line.Quantity, line.UnitPrice, line.Amount, u.Currency); err != nil {
			return OverageResult{}, err
		}
	}
	if len(quote.Lines) > 0 {
		// 水位推进到当前用量：下次只对新增部分收费。
		if _, err := tx.Exec(ctx, `UPDATE services SET billed_disk_mb=$2,billed_bw_gb=$3,updated_at=now() WHERE id=$1`,
			u.ServiceID, u.UsageDiskMB, u.UsageBWG); err != nil {
			return OverageResult{}, err
		}
	}
	if quote.OverHardLimit {
		if _, err := tx.Exec(ctx, `UPDATE services SET overage_suspended_at=now(),updated_at=now() WHERE id=$1 AND overage_suspended_at IS NULL`, u.ServiceID); err != nil {
			return OverageResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return OverageResult{}, err
	}
	if res.TotalCents > 0 {
		res.Message = fmt.Sprintf("新增超量费用 %d 分", res.TotalCents)
	} else {
		res.Message = "本周期没有新增超量用量"
	}
	return res, nil
}

// ServiceUsage 是一个服务的用量与水位（供 API 读取）。
type ServiceUsage struct {
	DiskMB       int64      `json:"disk_mb"`
	BWG          int64      `json:"bw_gb"`
	BilledDiskMB int64      `json:"billed_disk_mb"`
	BilledBWG    int64      `json:"billed_bw_gb"`
	ReportedAt   *time.Time `json:"reported_at,omitempty"`
}

// ServiceUsage 读取一个服务的当前用量。
func (s *Store) ServiceUsage(ctx context.Context, servicePublicID string) (ServiceUsage, error) {
	var u ServiceUsage
	err := s.DB.QueryRow(ctx, `SELECT usage_disk_mb,usage_bw_gb,billed_disk_mb,billed_bw_gb,usage_reported_at FROM services WHERE public_id=$1`, servicePublicID).
		Scan(&u.DiskMB, &u.BWG, &u.BilledDiskMB, &u.BilledBWG, &u.ReportedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return u, ErrNotFound
	}
	return u, err
}

// OverageCharge 是一条超量账单明细。
type OverageCharge struct {
	PublicID  string    `json:"id"`
	Resource  string    `json:"resource"`
	Quantity  int64     `json:"quantity"`
	UnitPrice int64     `json:"unit_price_cents"`
	Amount    int64     `json:"amount_cents"`
	Currency  string    `json:"currency"`
	CreatedAt time.Time `json:"created_at"`
}

// ListOverageCharges 列出一个服务的超量账单（空 servicePublicID 表示全站）。
func (s *Store) ListOverageCharges(ctx context.Context, servicePublicID string, limit int) ([]OverageCharge, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	if servicePublicID == "" {
		rows, err := s.DB.Query(ctx, `SELECT public_id::text,resource,quantity,unit_price_cents,amount_cents,currency,created_at FROM overage_charges ORDER BY id DESC LIMIT $1`, limit)
		if err != nil {
			return nil, err
		}
		return scanOverageCharges(rows)
	}
	rows, err := s.DB.Query(ctx, `SELECT c.public_id::text,c.resource,c.quantity,c.unit_price_cents,c.amount_cents,c.currency,c.created_at
FROM overage_charges c JOIN services s ON s.id=c.service_id
WHERE s.public_id=$1 ORDER BY c.id DESC LIMIT $2`, servicePublicID, limit)
	if err != nil {
		return nil, err
	}
	return scanOverageCharges(rows)
}

func scanOverageCharges(rows pgx.Rows) ([]OverageCharge, error) {
	defer rows.Close()
	out := []OverageCharge{}
	for rows.Next() {
		var c OverageCharge
		if err := rows.Scan(&c.PublicID, &c.Resource, &c.Quantity, &c.UnitPrice, &c.Amount, &c.Currency, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
