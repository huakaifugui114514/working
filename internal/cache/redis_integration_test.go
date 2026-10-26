//go:build integration

package cache

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestRedisIntegration(t *testing.T) {
	addr := os.Getenv("KF_REDIS_ADDR")
	if addr == "" {
		t.Skip("未设置 KF_REDIS_ADDR，跳过 Redis 集成测试")
	}
	c, err := NewRedis(RedisOptions{Addr: addr, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	defer func() { _ = c.Close() }()
	ctx := context.Background()
	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	key := "test:cache:" + time.Now().Format("150405.000000")
	t.Cleanup(func() {
		_, _ = c.client.Del(context.Background(), key, key+":n").Result()
	})

	if _, err := c.Get(ctx, key); !errors.Is(err, ErrMiss) {
		t.Fatalf("Get miss = %v, want ErrMiss", err)
	}
	if err := c.Set(ctx, key, []byte("v"), time.Minute); err != nil {
		t.Fatalf("Set: %v", err)
	}
	raw, err := c.Get(ctx, key)
	if err != nil || string(raw) != "v" {
		t.Fatalf("Get = %q, %v", raw, err)
	}
	if n, err := c.Incr(ctx, key+":n"); err != nil || n != 1 {
		t.Fatalf("Incr = %d, %v", n, err)
	}
}
