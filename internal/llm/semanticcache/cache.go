// Package semanticcache 是进程内语义缓存：query 向量余弦相似度命中则复用答案。
//
// 它是可选加速层，不是事实源；embedding 失败时 fail-open，继续调 LLM。
package semanticcache

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// Embedder 把文本转成向量；与 internal/embedding.Embedder 兼容。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// Cache 是最小语义缓存。
type Cache struct {
	mu        sync.Mutex
	embedder  Embedder
	threshold float64
	ttl       time.Duration
	max       int
	entries   []entry
}

type entry struct {
	vector  []float32
	answer  string
	expires time.Time
}

// New 创建缓存。
func New(embedder Embedder, threshold float64, ttl time.Duration, max int) (*Cache, error) {
	if embedder == nil {
		return nil, errors.New("semanticcache: embedder 不能为空")
	}
	if threshold < 0 || threshold > 1 {
		return nil, fmt.Errorf("semanticcache: threshold 必须在 0~1，当前 %v", threshold)
	}
	if ttl <= 0 || max < 1 {
		return nil, fmt.Errorf("semanticcache: ttl/max 必须为正数")
	}
	return &Cache{embedder: embedder, threshold: threshold, ttl: ttl, max: max}, nil
}

// Get 返回语义相似的历史答案。
func (c *Cache) Get(ctx context.Context, query string) (string, bool) {
	if query == "" {
		return "", false
	}
	vecs, err := c.embedder.Embed(ctx, []string{query})
	if err != nil || len(vecs) != 1 || len(vecs[0]) == 0 {
		return "", false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	best := -1.0
	answer := ""
	for _, e := range c.entries {
		score := cosine(vecs[0], e.vector)
		if score > best {
			best = score
			answer = e.answer
		}
	}
	if best >= c.threshold {
		return answer, true
	}
	return "", false
}

// Put 写入一条缓存；embedding 失败时返回错误，由调用方决定是否记录。
func (c *Cache) Put(ctx context.Context, query, answer string) error {
	if query == "" || answer == "" {
		return nil
	}
	vecs, err := c.embedder.Embed(ctx, []string{query})
	if err != nil {
		return err
	}
	if len(vecs) != 1 || len(vecs[0]) == 0 {
		return errors.New("semanticcache: embedding 为空")
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.prune(now)
	if len(c.entries) >= c.max {
		c.entries = c.entries[1:]
	}
	c.entries = append(c.entries, entry{vector: vecs[0], answer: answer, expires: now.Add(c.ttl)})
	return nil
}

func (c *Cache) prune(now time.Time) {
	i := 0
	for i < len(c.entries) && c.entries[i].expires.Before(now) {
		i++
	}
	if i > 0 {
		c.entries = c.entries[i:]
	}
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return -1
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return -1
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
