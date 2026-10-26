package knowledge

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	// MaxDocumentContentBytes 是单篇正文的 UTF-8 字节上限。
	// HTTP 请求体上限会略大（JSON 包装），见 rest/content.go 的契约。
	MaxDocumentContentBytes = 1 << 20

	// MaxDocumentChunks 是单篇文档允许的最大 chunk 数。
	// 超过时 service 返回 invalid_argument，避免一次请求写入过多行。
	MaxDocumentChunks = 2000

	// ContentChunkerVersion 是当前分块算法的版本。
	// 修改 max/overlap/断句规则时递增；repository 用它判断是否需要重新切块。
	ContentChunkerVersion = 1
)

var (
	// ErrContentEmpty 表示正文为空或只有空白。
	ErrContentEmpty = errors.New("knowledge: 正文不能为空")

	// ErrContentTooLarge 表示正文超过 MaxDocumentContentBytes。
	ErrContentTooLarge = errors.New("knowledge: 正文超过大小上限")

	// ErrTooManyChunks 表示分块数量超过 MaxDocumentChunks。
	ErrTooManyChunks = errors.New("knowledge: 分块数量超过上限")
)

// ContentChunk 是分块后的一个片段。
//
// Index 从 0 开始且连续；Content 已去掉首尾空白；CharCount 是 Content 的 rune 数。
type ContentChunk struct {
	Index     int
	Content   string
	CharCount int
}

// StoredChunk 是数据库中已持久化的一个 chunk。
//
// 和 ContentChunk 的区别：ContentChunk 是切块结果（还没有 id），
// StoredChunk 带数据库生成的 UUID，embedding worker 用它作为 Qdrant point id。
type StoredChunk struct {
	ID        uuid.UUID
	Index     int
	Content   string
	CharCount int
}

// DocumentContent 是一篇文档的正文元数据。
//
// ContentHash 是完整正文的 SHA-256；ChunkerVersion 记录切块算法版本。
type DocumentContent struct {
	DocumentID     uuid.UUID
	Content        string
	ContentHash    [32]byte
	ChunkerVersion int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ContentRepository 是文档正文与分块的持久化接口。
//
// 契约：
//   - ReplaceDocumentContent 必须在一个事务里锁住 document 行（deleted_at IS NULL），
//     不存在/已软删 → ErrNotFound
//   - 先删旧 chunks，再 upsert document_contents，再批量插入新 chunks
//   - 同一事务内 upsert embedding_jobs（content_hash 最新的 pending 任务）
//   - 时间全部 UTC，毫秒精度；chunk id 由实现生成（uuid.NewV7）
//   - GetDocumentContent 只返回未软删文档的正文；不存在 → ErrNotFound
type ContentRepository interface {
	ReplaceDocumentContent(
		ctx context.Context,
		documentID uuid.UUID,
		content string,
		hash [32]byte,
		chunks []ContentChunk,
	) error

	GetDocumentContent(ctx context.Context, documentID uuid.UUID) (*DocumentContent, error)

	// ListDocumentChunks 按 chunk_index 升序返回当前 chunks。
	// 文档不存在/已软删时返回空切片（worker 已经先用 GetDocumentContent 判断）。
	ListDocumentChunks(ctx context.Context, documentID uuid.UUID) ([]StoredChunk, error)
}
