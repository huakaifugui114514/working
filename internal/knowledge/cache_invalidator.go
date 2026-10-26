package knowledge

import "context"

// CacheVersioner 是写路径递增搜索缓存版本的接口。
type CacheVersioner interface {
	Incr(ctx context.Context, key string) (int64, error)
}

func bumpSearchCache(ctx context.Context, v CacheVersioner) {
	if v == nil {
		return
	}
	// 缓存失效失败不能影响写请求；TTL 兜底。
	_, _ = v.Incr(ctx, SearchCacheVersionKey)
}
