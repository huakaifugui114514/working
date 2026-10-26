package gateway

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrQuotaExceeded 表示请求数或 token 配额已用尽。
var ErrQuotaExceeded = errors.New("gateway: quota exceeded")

// Quota 是进程内滑动窗口配额。
//
// 单副本有效；多副本需要共享 Redis/网关限流。当前先保证不下意识打爆 provider。
type Quota struct {
	mu           sync.Mutex
	maxPerMinute int
	maxTokensDay int
	window       []time.Time
	day          string
	tokens       int
}

// NewQuota 创建配额；两个上限都 <=0 表示不限制（返回 nil）。
func NewQuota(maxPerMinute, maxTokensPerDay int) *Quota {
	if maxPerMinute <= 0 && maxTokensPerDay <= 0 {
		return nil
	}
	return &Quota{maxPerMinute: maxPerMinute, maxTokensDay: maxTokensPerDay, day: time.Now().UTC().Format("2006-01-02")}
}

// Allow 检查请求数；成功时占用一个窗口槽位。
func (q *Quota) Allow(now time.Time) error {
	if q == nil {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.maxPerMinute > 0 {
		cutoff := now.Add(-time.Minute)
		i := 0
		for i < len(q.window) && q.window[i].Before(cutoff) {
			i++
		}
		q.window = q.window[i:]
		if len(q.window) >= q.maxPerMinute {
			return fmt.Errorf("%w: requests_per_minute=%d", ErrQuotaExceeded, q.maxPerMinute)
		}
		q.window = append(q.window, now)
	}
	return nil
}

// AddTokens 记录 token 消耗；返回是否超过日配额。
func (q *Quota) AddTokens(now time.Time, tokens int) error {
	if q == nil || q.maxTokensDay <= 0 || tokens <= 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	day := now.UTC().Format("2006-01-02")
	if day != q.day {
		q.day = day
		q.tokens = 0
	}
	q.tokens += tokens
	if q.tokens > q.maxTokensDay {
		return fmt.Errorf("%w: tokens_per_day=%d", ErrQuotaExceeded, q.maxTokensDay)
	}
	return nil
}
