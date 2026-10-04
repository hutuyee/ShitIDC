package cache

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

func Open(ctx context.Context, addr, password string, db int) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{Addr: addr, Password: password, DB: db, DialTimeout: 3 * time.Second})
	ctxPing, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := client.Ping(ctxPing).Err(); err != nil {
		_ = client.Close()
		return nil, err
	}
	return client, nil
}
