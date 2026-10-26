package knowledge

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/apperr"
	"github.com/huakaifugui114514/working/internal/cache"
)

const (
	// DefaultSearchLimit 是检索 API 的默认每页条数；handler 在缺省时使用。
	DefaultSearchLimit = 20

	// MaxSearchLimit 是单次检索允许的最大条数。
	MaxSearchLimit = 50

	// MaxSearchWindow 是 offset+limit 的窗口上限。
	// 搜索按 score 排序，offset 深了会越来越慢；MVP 只允许看前 1000 条。
	MaxSearchWindow = 1000

	// MinSearchQueryRunes 是最短查询长度。
	// 这是产品精度规则：单字结果太宽；不是 MySQL ngram 一定查不到。
	MinSearchQueryRunes = 2

	// MaxSearchQueryRunes 是最长查询长度，防止超长查询打满 FULLTEXT。
	MaxSearchQueryRunes = 256
)

// SearchResult 是一页检索结果。
//
// NextOffset 为 nil 表示没有下一页，或已经到达 MaxSearchWindow 窗口上限。
type SearchResult struct {
	Items      []SearchHit
	NextOffset *int
}

// CollectionLookup 是 SearchService 需要的 collection 存在性检查。
//
// 只取一个方法，不把整个 Repository 依赖进来；*Service 已满足这个接口。
type CollectionLookup interface {
	GetCollection(ctx context.Context, id uuid.UUID) (*Collection, error)
}

// SearchService 是检索的业务层。
//
// 职责边界：q/limit/offset 校验、collection 存在性、next_offset 计算、错误翻译；
// 不写 SQL、不处理 HTTP/JSON。
type SearchService struct {
	search      SearchRepository
	collections CollectionLookup
	cache       cache.Cache
	cacheTTL    time.Duration
}

// SearchCacheVersionKey 是搜索结果缓存版本号。
//
// 写路径（正文/文档/collection）递增它，读路径把版本放进 cache key；
// 这样不需要按 pattern 删除缓存，也不会因为 Redis SCAN 阻塞线上。
const SearchCacheVersionKey = "search:version"

// NewSearchService 注入检索仓储与 collection 查询。两者都不能为 nil。
func NewSearchService(search SearchRepository, collections CollectionLookup) *SearchService {
	if search == nil {
		panic("knowledge.NewSearchService: search is nil")
	}
	if collections == nil {
		panic("knowledge.NewSearchService: collections is nil")
	}
	return &SearchService{search: search, collections: collections}
}

// WithCache 启用搜索结果缓存；ttl <= 0 表示关闭。
func (s *SearchService) WithCache(c cache.Cache, ttl time.Duration) *SearchService {
	if c == nil || ttl <= 0 {
		return s
	}
	s.cache = c
	s.cacheTTL = ttl
	return s
}

func (s *SearchService) cacheVersion(ctx context.Context) int64 {
	if s.cache == nil {
		return 0
	}
	raw, err := s.cache.Get(ctx, SearchCacheVersionKey)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func searchCacheKey(version int64, q string, collectionID *uuid.UUID, limit, offset int) string {
	collection := ""
	if collectionID != nil {
		collection = collectionID.String()
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%d", q, collection, limit, offset)))
	return fmt.Sprintf("search:v%d:%x", version, sum[:])
}

// Search 执行一次 chunk 级检索。
//
// 契约：
//   - q trim 后 2..256 个 rune
//   - limit 1..50
//   - offset >= 0，且 offset+limit <= 1000
//   - collectionID 非 nil 时，collection 不存在/已软删 → not_found
//   - 其他错误 → internal_error
func (s *SearchService) Search(
	ctx context.Context,
	q string,
	collectionID *uuid.UUID,
	limit int,
	offset int,
) (*SearchResult, error) {
	q = strings.TrimSpace(q)

	queryRunes := utf8.RuneCountInString(q)
	if queryRunes == 0 {
		return nil, apperr.New(apperr.CodeInvalidArgument, "q 不能为空")
	}
	if queryRunes < MinSearchQueryRunes || queryRunes > MaxSearchQueryRunes {
		return nil, apperr.New(
			apperr.CodeInvalidArgument,
			fmt.Sprintf("q 长度必须在 %d~%d 个字符之间", MinSearchQueryRunes, MaxSearchQueryRunes),
		)
	}

	if limit < 1 || limit > MaxSearchLimit {
		return nil, apperr.New(
			apperr.CodeInvalidArgument,
			fmt.Sprintf("limit 必须在 1~%d 之间", MaxSearchLimit),
		)
	}
	if offset < 0 || offset > MaxSearchWindow || offset+limit > MaxSearchWindow {
		return nil, apperr.New(
			apperr.CodeInvalidArgument,
			fmt.Sprintf("offset 必须 >= 0，且 offset+limit 不能超过 %d", MaxSearchWindow),
		)
	}

	if collectionID != nil {
		if *collectionID == uuid.Nil {
			return nil, apperr.New(apperr.CodeInvalidArgument, "collection_id 不能为空")
		}
		if _, err := s.collections.GetCollection(ctx, *collectionID); err != nil {
			if errors.Is(err, ErrNotFound) {
				return nil, apperr.Wrap(err, apperr.CodeNotFound, "知识库不存在")
			}
			return nil, apperr.Wrap(err, apperr.CodeInternal, "查询知识库失败")
		}
	}

	cacheVersion := int64(0)
	if s.cache != nil {
		cacheVersion = s.cacheVersion(ctx)
		key := searchCacheKey(cacheVersion, q, collectionID, limit, offset)
		if raw, err := s.cache.Get(ctx, key); err == nil {
			var cached SearchResult
			if json.Unmarshal(raw, &cached) == nil {
				return &cached, nil
			}
		}
	}

	hits, hasMore, err := s.search.Search(ctx, q, collectionID, limit, offset)
	if err != nil {
		return nil, apperr.Wrap(err, apperr.CodeInternal, "搜索失败")
	}
	if hits == nil {
		hits = []SearchHit{}
	}

	var nextOffset *int
	if hasMore && offset+limit < MaxSearchWindow {
		next := offset + limit
		nextOffset = &next
	}

	result := &SearchResult{Items: hits, NextOffset: nextOffset}
	if s.cache != nil {
		if raw, err := json.Marshal(result); err == nil {
			key := searchCacheKey(cacheVersion, q, collectionID, limit, offset)
			_ = s.cache.Set(ctx, key, raw, s.cacheTTL)
		}
	}
	return result, nil
}
