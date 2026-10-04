package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// debitWalletForGroup 为一次合并结算扣一次款。
//
// 抽成函数是为了让「余额检查 → 扣减 → 记流水」三件事必须成组出现：
// 只扣不记流水会让对账对不上，只记流水不扣钱更严重。
// 幂等键取自结算批次，重复调用不会重复扣。
func debitWalletForGroup(ctx context.Context, tx pgx.Tx, userID int64, currency string, amount int64, idempotencyKey string) error {
	if amount <= 0 {
		return nil
	}
	var accountID, balance int64
	if err := tx.QueryRow(ctx,
		`SELECT id,balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency=$2 FOR UPDATE`,
		userID, currency).Scan(&accountID, &balance); err != nil {
		return err
	}
	if balance < amount {
		return ErrInsufficientBalance
	}
	newBalance := balance - amount
	if _, err := tx.Exec(ctx,
		`UPDATE wallet_accounts SET balance_cents=$1,updated_at=now() WHERE id=$2`,
		newBalance, accountID); err != nil {
		return err
	}
	ref := "checkout:" + idempotencyKey
	if len(ref) > 120 {
		ref = ref[:120]
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO wallet_transactions(account_id,type,amount_cents,balance_before_cents,balance_after_cents,currency,reference_type,reference_id,description,idempotency_key)
VALUES($1,'debit',$2,$3,$4,$5,'checkout',$6,$7,$8)`,
		accountID, amount, balance, newBalance, currency, ref, "购物车合并结算", ref); err != nil {
		return fmt.Errorf("记录钱包流水失败: %w", err)
	}
	return nil
}
