package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// debitWalletTx 从钱包扣一笔钱并记流水。
//
// 抽成函数是为了让「余额检查 → 扣减 → 记流水」三件事必须成组出现：
// 只扣钱不记流水会让对账对不上，只记流水不扣钱更严重。
// referenceType/referenceID 说明这笔钱的用途，idempotencyKey 供排查重复扣款。
//
// 记账符号约定：**支出记为负数**、入账记正数。这样「所有负数流水之和」就是总共花掉的钱，
// 对账查询不需要知道 type 的含义，只按符号算即可。credit 方向的流水（充值/退款/返佣）
// 都写正数，两边口径一致。
func debitWalletTx(ctx context.Context, tx pgx.Tx, userID int64, currency string, amount int64, referenceType, referenceID, idempotencyKey string) error {
	if amount < 0 {
		return fmt.Errorf("扣款金额不能为负数")
	}
	if amount == 0 {
		// 0 元订单（免费/试用）不需要动余额，也不该产生一条 0 元流水。
		return nil
	}
	var accountID, balance int64
	if err := tx.QueryRow(ctx,
		"SELECT id,balance_cents FROM wallet_accounts WHERE user_id=$1 AND currency=$2 FOR UPDATE",
		userID, currency).Scan(&accountID, &balance); err != nil {
		return err
	}
	if balance < amount {
		return ErrInsufficientBalance
	}
	newBalance := balance - amount
	if _, err := tx.Exec(ctx,
		"UPDATE wallet_accounts SET balance_cents=$1,updated_at=now() WHERE id=$2",
		newBalance, accountID); err != nil {
		return err
	}
	key := "wallet:" + idempotencyKey
	if len(key) > 120 {
		key = key[:120]
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO wallet_transactions(account_id,type,amount_cents,balance_before_cents,balance_after_cents,currency,reference_type,reference_id,description,idempotency_key) VALUES($1,'debit',$2,$3,$4,$5,$6,$7,$8,$9)",
		accountID, -amount, balance, newBalance, currency, referenceType, referenceID, "订单支付", key); err != nil {
		return fmt.Errorf("记录钱包流水失败: %w", err)
	}
	return nil
}
