// Package cache 提供可选的 Redis 缓存。
//
// 缓存不是事实源：Redis 故障时调用方应 fail-open，继续查 MySQL。
package cache

import (
	"context"
	"errors"
	"time"
)

// ErrMiss 表示 key 不存在。
var ErrMiss = errors.New("cache: miss")

// Cache 是知识库需要的缓存操作子集。
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Incr(ctx context.Context, key string) (int64, error)
	Ping(ctx context.Context) error
	Close() error
}
