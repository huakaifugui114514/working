package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisOptions 是 Redis 连接配置。
type RedisOptions struct {
	Addr          string
	SentinelAddrs []string
	MasterName    string
	Password      string
	DB            int
	Timeout       time.Duration
}

// Redis 是 go-redis 实现。
type Redis struct {
	client *redis.Client
}

// NewRedis 校验配置并构造客户端（不主动 Ping）。
func NewRedis(opts RedisOptions) (*Redis, error) {
	if opts.Addr == "" && len(opts.SentinelAddrs) == 0 {
		return nil, fmt.Errorf("cache: addr 或 sentinel_addrs 必须配置")
	}
	if len(opts.SentinelAddrs) > 0 && opts.MasterName == "" {
		return nil, fmt.Errorf("cache: 使用 sentinel 时 master_name 不能为空")
	}
	if opts.Timeout <= 0 {
		return nil, fmt.Errorf("cache: timeout 必须为正数")
	}
	if opts.DB < 0 {
		return nil, fmt.Errorf("cache: db 不能为负数")
	}
	var client *redis.Client
	if len(opts.SentinelAddrs) > 0 {
		client = redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    opts.MasterName,
			SentinelAddrs: opts.SentinelAddrs,
			Password:      opts.Password,
			DB:            opts.DB,
			DialTimeout:   opts.Timeout,
			ReadTimeout:   opts.Timeout,
			WriteTimeout:  opts.Timeout,
		})
	} else {
		client = redis.NewClient(&redis.Options{
			Addr:         opts.Addr,
			Password:     opts.Password,
			DB:           opts.DB,
			DialTimeout:  opts.Timeout,
			ReadTimeout:  opts.Timeout,
			WriteTimeout: opts.Timeout,
		})
	}
	return &Redis{client: client}, nil
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, error) {
	raw, err := r.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, ErrMiss
	}
	if err != nil {
		return nil, fmt.Errorf("cache: get %s: %w", key, err)
	}
	return raw, nil
}

func (r *Redis) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("cache: ttl 必须为正数")
	}
	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("cache: set %s: %w", key, err)
	}
	return nil
}

func (r *Redis) Incr(ctx context.Context, key string) (int64, error) {
	n, err := r.client.Incr(ctx, key).Result()
	if err != nil {
		return 0, fmt.Errorf("cache: incr %s: %w", key, err)
	}
	return n, nil
}

func (r *Redis) Ping(ctx context.Context) error {
	if err := r.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("cache: ping: %w", err)
	}
	return nil
}

func (r *Redis) Close() error { return r.client.Close() }
