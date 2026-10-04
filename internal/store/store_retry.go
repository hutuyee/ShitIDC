package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5/pgconn"
)

// 并发下的序列化重试。
//
// 订单与钱包相关的写操作跑在 SERIALIZABLE 隔离级别下：这是保证“余额不会被扣成
// 负数”“库存不会被超卖”的最强保证，代价是 PostgreSQL 在检测到读写冲突时会用
// 40001（serialization_failure）中止事务。官方推荐的处理方式就是客户端重试，
// 这里把重试收敛到一个辅助函数里，避免每个调用方各写一遍。

// IsSerializationFailure 判断错误是否为可重试的序列化冲突（SQLSTATE 40001）。
func IsSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgerrcode.SerializationFailure {
		return true
	}
	// 少数驱动/包装层会把 40001 变成文本，兜底识别一次。
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 40001")
}

// retrySerializable 重试一个事务型操作，直到成功、遇到不可重试的错误或用尽次数。
// 退避采用 2ms 起步的线性增长并带一点抖动，避免多个并发事务同步重试再次撞车。
func retrySerializable(ctx context.Context, attempts int, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}
	var err error
	for i := 0; i < attempts; i++ {
		if err = fn(); err == nil {
			return nil
		}
		if !IsSerializationFailure(err) {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 最后一次失败不再等待，直接返回。
		if i == attempts-1 {
			break
		}
		backoff := time.Duration(i+1) * 2 * time.Millisecond
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
	}
	return err
}
