package semanticcache

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeEmbedder struct {
	vectors map[string][]float32
	err     error
}

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	out := make([][]float32, 0, len(texts))
	for _, text := range texts {
		out = append(out, f.vectors[text])
	}
	return out, nil
}

func TestCacheHitMiss(t *testing.T) {
	emb := &fakeEmbedder{vectors: map[string][]float32{
		"q1": {1, 0},
		"q2": {0.99, 0.1},
		"q3": {0, 1},
	}}
	c, err := New(emb, 0.9, time.Minute, 10)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()
	if err := c.Put(ctx, "q1", "answer"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, ok := c.Get(ctx, "q2"); !ok || got != "answer" {
		t.Fatalf("semantic hit = %q, %v", got, ok)
	}
	if _, ok := c.Get(ctx, "q3"); ok {
		t.Fatal("不相似 query 不应命中")
	}
}

func TestCacheExpiryAndMax(t *testing.T) {
	emb := &fakeEmbedder{vectors: map[string][]float32{
		"a": {1, 0}, "b": {0, 1}, "c": {1, 1},
	}}
	c, _ := New(emb, 0.99, 10*time.Millisecond, 1)
	ctx := context.Background()
	_ = c.Put(ctx, "a", "A")
	if err := c.Put(ctx, "b", "B"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got, ok := c.Get(ctx, "a"); ok {
		t.Fatalf("max=1 应淘汰旧条目，got %q", got)
	}
	time.Sleep(15 * time.Millisecond)
	if _, ok := c.Get(ctx, "b"); ok {
		t.Fatal("过期条目不应命中")
	}
}

func TestCacheFailOpen(t *testing.T) {
	emb := &fakeEmbedder{err: errors.New("embedding down")}
	c, _ := New(emb, 0.9, time.Minute, 10)
	ctx := context.Background()
	if _, ok := c.Get(ctx, "q"); ok {
		t.Fatal("embedding 失败应 miss")
	}
	if err := c.Put(ctx, "q", "a"); err == nil {
		t.Fatal("Put 应返回错误")
	}
}

func TestNewValidation(t *testing.T) {
	emb := &fakeEmbedder{}
	tests := []struct {
		threshold float64
		ttl       time.Duration
		max       int
	}{
		{0.9, 0, 1},
		{0.9, time.Minute, 0},
		{-1, time.Minute, 1},
		{2, time.Minute, 1},
	}
	for _, tt := range tests {
		if _, err := New(emb, tt.threshold, tt.ttl, tt.max); err == nil {
			t.Fatalf("threshold=%v ttl=%v max=%d 应失败", tt.threshold, tt.ttl, tt.max)
		}
	}
	if _, err := New(nil, 0.9, time.Minute, 1); err == nil {
		t.Fatal("nil embedder 应失败")
	}
}
