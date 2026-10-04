package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// 后付费（对应魔方 pay_method = postpaid）。
//
// 语义：授信客户先开通、后付款，到期未付则暂停服务。
//
// 「授信」是这个功能的核心风险点，所以额度是**显式授予**的：
//   * postpaid_enabled 默认 FALSE —— 注册不会自动获得赊账能力
//   * 未付占用**实时算**（不另存冗余字段），避免「额度字段与实际占用不一致」
//     这种最难查的账目问题

// CreditAccount 是一个用户的授信状况。
type CreditAccount struct {
	UserID     int64 `json:"user_id"`
	Enabled    bool  `json:"enabled"`
	LimitCents int64 `json:"limit_cents"`
	Days       int   `json:"credit_days"`
	// UsedCents 是当前未付的后付费订单金额之和（占用中的额度）。
	UsedCents int64 `json:"used_cents"`
	// AvailableCents = LimitCents - UsedCents，不会小于 0。
	AvailableCents int64 `json:"available_cents"`
	// OverdueCents 是已经过期未付的金额（逾期部分）。
	OverdueCents int64 `json:"overdue_cents"`
	// OverdueCount 是逾期未付的订单笔数。
	OverdueCount int `json:"overdue_count"`
}

// ErrCreditLimitExceeded 表示超出授信额度。
var ErrCreditLimitExceeded = errors.New("超出授信额度")

// ErrPostpaidNotEnabled 表示该用户没有后付费权限。
var ErrPostpaidNotEnabled = errors.New("该账号未开通后付费")

// CreditAccount 读出一个用户的授信状况（含实时占用）。
func (s *Store) CreditAccount(ctx context.Context, userID int64) (CreditAccount, error) {
	var c CreditAccount
	err := s.DB.QueryRow(ctx, `SELECT id,postpaid_enabled,credit_limit_cents,credit_days FROM users WHERE id=$1`, userID).
		Scan(&c.UserID, &c.Enabled, &c.LimitCents, &c.Days)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	// 占用 = 未付的后付费订单总额。实时统计，不存冗余。
	// 金额必须写成 o.total_cents：orders 与 invoices 都有 total_cents，不限定就是 42702。
	if err := s.DB.QueryRow(ctx, `SELECT
  coalesce(sum(o.total_cents),0),
  coalesce(sum(o.total_cents) FILTER (WHERE i.due_at < now()),0),
  count(*) FILTER (WHERE i.due_at < now())
FROM orders o
LEFT JOIN invoices i ON i.order_id=o.id
WHERE o.user_id=$1 AND o.pay_method='postpaid' AND o.status='unpaid'`, userID).
		Scan(&c.UsedCents, &c.OverdueCents, &c.OverdueCount); err != nil {
		return c, err
	}
	c.AvailableCents = c.LimitCents - c.UsedCents
	if c.AvailableCents < 0 {
		c.AvailableCents = 0
	}
	return c, nil
}

// checkPostpaidTx 在订单事务内校验后付费是否可用并占用额度。
//
// 用 FOR UPDATE 锁住用户行：并发下单必须串行判断额度，否则两笔订单可能各自看到
// 「还有余额」而一起通过，把额度用超。这是整个后付费最关键的一致性点。
func (s *Store) checkPostpaidTx(ctx context.Context, tx pgx.Tx, userID int64, amountCents int64) (creditDays int, err error) {
	var enabled bool
	var limit int64
	var days int
	err = tx.QueryRow(ctx, `SELECT postpaid_enabled,credit_limit_cents,credit_days FROM users WHERE id=$1 FOR UPDATE`, userID).
		Scan(&enabled, &limit, &days)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, err
	}
	if !enabled || limit <= 0 {
		return 0, ErrPostpaidNotEnabled
	}
	var used int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(sum(total_cents),0) FROM orders
WHERE user_id=$1 AND pay_method='postpaid' AND status='unpaid'`, userID).Scan(&used); err != nil {
		return 0, err
	}
	if used+amountCents > limit {
		return 0, fmt.Errorf("%w：已用 %d 分，本次 %d 分，额度 %d 分", ErrCreditLimitExceeded, used, amountCents, limit)
	}
	if days <= 0 {
		days = 30
	}
	return days, nil
}

// SetCredit 调整一个用户的授信（管理员操作，留痕）。
func (s *Store) SetCredit(ctx context.Context, userPublicID string, enabled bool, limitCents int64, days int, note string, actorID int64) error {
	if limitCents < 0 {
		return fmt.Errorf("额度不能为负数")
	}
	if days < 0 || days > 365 {
		return fmt.Errorf("账期必须在 0 到 365 天之间")
	}
	if !enabled {
		// 停用授信前必须结清欠款：否则等于把风险直接留在账上。
		var used int64
		if err := s.DB.QueryRow(ctx, `SELECT coalesce(sum(o.total_cents),0) FROM orders o
JOIN users u ON u.id=o.user_id
WHERE u.public_id=$1 AND o.pay_method='postpaid' AND o.status='unpaid'`, userPublicID).Scan(&used); err != nil {
			return err
		}
		if used > 0 {
			return fmt.Errorf("该账号还有 %d 分未付的后付费订单，请先结清再停用授信", used)
		}
	}
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var userID, before int64
	if err := tx.QueryRow(ctx, `SELECT id,credit_limit_cents FROM users WHERE public_id=$1 FOR UPDATE`, userPublicID).
		Scan(&userID, &before); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET postpaid_enabled=$2,credit_limit_cents=$3,credit_days=$4,updated_at=now() WHERE id=$1`,
		userID, enabled, limitCents, days); err != nil {
		return err
	}
	event := "grant"
	switch {
	case !enabled:
		event = "disable"
	case before == 0 && limitCents > 0:
		event = "enable"
	case limitCents < before:
		event = "revoke"
	}
	var actor any
	if actorID > 0 {
		actor = actorID
	}
	if _, err := tx.Exec(ctx, `INSERT INTO credit_events(user_id,event,before_limit_cents,after_limit_cents,note,actor_user_id)
VALUES($1,$2,$3,$4,$5,$6)`, userID, event, before, limitCents, strings.TrimSpace(note), actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ListCreditAccounts 列出所有已开通后付费的账号及其占用情况。
func (s *Store) ListCreditAccounts(ctx context.Context) ([]CreditAccount, error) {
	rows, err := s.DB.Query(ctx, `SELECT u.id,u.postpaid_enabled,u.credit_limit_cents,u.credit_days,
  coalesce((SELECT sum(o.total_cents) FROM orders o WHERE o.user_id=u.id AND o.pay_method='postpaid' AND o.status='unpaid'),0) AS used,
  coalesce((SELECT sum(o.total_cents) FROM orders o JOIN invoices i ON i.order_id=o.id
            WHERE o.user_id=u.id AND o.pay_method='postpaid' AND o.status='unpaid' AND i.due_at < now()),0) AS overdue
FROM users u WHERE u.postpaid_enabled = TRUE ORDER BY overdue DESC, u.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CreditAccount{}
	for rows.Next() {
		var c CreditAccount
		if err := rows.Scan(&c.UserID, &c.Enabled, &c.LimitCents, &c.Days, &c.UsedCents, &c.OverdueCents); err != nil {
			return nil, err
		}
		c.AvailableCents = c.LimitCents - c.UsedCents
		if c.AvailableCents < 0 {
			c.AvailableCents = 0
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// OverduePostpaidService 是一个因欠款需要暂停的服务。
type OverduePostpaidService struct {
	ServiceID   string    `json:"service_id"`
	UserID      int64     `json:"-"`
	OrderID     string    `json:"order_id"`
	AmountCents int64     `json:"amount_cents"`
	DueAt       time.Time `json:"due_at"`
	DaysOverdue int       `json:"days_overdue"`
}
