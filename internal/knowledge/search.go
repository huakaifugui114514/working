package knowledge

import (
	"context"

	"github.com/google/uuid"
)

// SearchHit 是一条 chunk 级检索结果。
//
// Score 是 MySQL FULLTEXT 的相关度，只在同一次查询内用于排序；
// 不承诺跨 MySQL 版本稳定，也不作为业务指标。
type SearchHit struct {
	ChunkID       uuid.UUID
	DocumentID    uuid.UUID
	DocumentTitle string
	ChunkIndex    int
	Content       string
	Score         float64
}

// SearchRepository 是 chunk 检索的持久化接口。
//
// 契约：
//   - query 由调用方 trim 并校验长度；实现不重复做业务校验
//   - collectionID == nil 表示全局检索；非 nil 时只返回该 collection 的 chunk
//   - limit / offset 由调用方校验；实现按 limit+1 取行判断是否还有下一页
//   - 只返回未软删 document 的 chunk；collection 已软删时返回空结果
//   - 没有匹配时返回空切片和 false，不返回 ErrNotFound
//   - 结果按 (score DESC, chunk_id DESC) 排序
type SearchRepository interface {
	Search(
		ctx context.Context,
		query string,
		collectionID *uuid.UUID,
		limit int,
		offset int,
	) (hits []SearchHit, hasMore bool, err error)
}
