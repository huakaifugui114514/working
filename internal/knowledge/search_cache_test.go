package knowledge

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/cache"
)

type fakeCache struct {
	values   map[string][]byte
	version  int64
	getErr   error
	setErr   error
	getCalls int
	setCalls int
	incrKey  string
}

func (f *fakeCache) Get(_ context.Context, key string) ([]byte, error) {
	f.getCalls++
	if f.getErr != nil {
		return nil, f.getErr
	}
	if key == SearchCacheVersionKey {
		return []byte(strconv.FormatInt(f.version, 10)), nil
	}
	v, ok := f.values[key]
	if !ok {
		return nil, cache.ErrMiss
	}
	return v, nil
}
func (f *fakeCache) Set(_ context.Context, key string, value []byte, _ time.Duration) error {
	f.setCalls++
	if f.setErr != nil {
		return f.setErr
	}
	if f.values == nil {
		f.values = make(map[string][]byte)
	}
	f.values[key] = value
	return nil
}
func (f *fakeCache) Incr(_ context.Context, key string) (int64, error) {
	f.incrKey = key
	f.version++
	return f.version, nil
}
func (f *fakeCache) Ping(context.Context) error { return nil }
func (f *fakeCache) Close() error               { return nil }

func TestSearchServiceCacheHitAndVersionBump(t *testing.T) {
	calls := 0
	repo := &fakeSearchRepository{search: func(context.Context, string, *uuid.UUID, int, int) ([]SearchHit, bool, error) {
		calls++
		return []SearchHit{{ChunkID: uuid.New(), DocumentTitle: "文档", Content: "正文"}}, false, nil
	}}
	fc := &fakeCache{values: make(map[string][]byte)}
	svc := NewSearchService(repo, &fakeCollectionLookup{}).WithCache(fc, time.Minute)

	if _, err := svc.Search(context.Background(), "知识库", nil, 5, 0); err != nil {
		t.Fatalf("first Search: %v", err)
	}
	if _, err := svc.Search(context.Background(), "知识库", nil, 5, 0); err != nil {
		t.Fatalf("second Search: %v", err)
	}
	if calls != 1 {
		t.Fatalf("repo calls = %d, want 1", calls)
	}
	if fc.setCalls == 0 {
		t.Fatal("缓存未写入")
	}

	_, _ = fc.Incr(context.Background(), SearchCacheVersionKey)
	if _, err := svc.Search(context.Background(), "知识库", nil, 5, 0); err != nil {
		t.Fatalf("third Search: %v", err)
	}
	if calls != 2 {
		t.Fatalf("版本变化后 repo calls = %d, want 2", calls)
	}
}

func TestSearchServiceCacheFailOpen(t *testing.T) {
	calls := 0
	repo := &fakeSearchRepository{search: func(context.Context, string, *uuid.UUID, int, int) ([]SearchHit, bool, error) {
		calls++
		return []SearchHit{}, false, nil
	}}
	fc := &fakeCache{getErr: errors.New("redis down"), setErr: errors.New("redis down")}
	svc := NewSearchService(repo, &fakeCollectionLookup{}).WithCache(fc, time.Minute)
	if _, err := svc.Search(context.Background(), "知识库", nil, 5, 0); err != nil {
		t.Fatalf("Search: %v", err)
	}
	if calls != 1 {
		t.Fatalf("repo calls = %d, want 1", calls)
	}
}

func TestContentServiceBumpsSearchCacheVersion(t *testing.T) {
	repo := &fakeContentRepository{replace: func(context.Context, uuid.UUID, string, [32]byte, []ContentChunk) error {
		return nil
	}}
	fc := &fakeCache{}
	svc := NewContentService(repo).WithCacheVersioner(fc)
	if err := svc.ReplaceDocumentContent(context.Background(), uuid.New(), "正文内容"); err != nil {
		t.Fatalf("ReplaceDocumentContent: %v", err)
	}
	if fc.incrKey != SearchCacheVersionKey {
		t.Fatalf("incr key = %q", fc.incrKey)
	}
}
