// Package knowledge 是知识库领域的核心：领域模型、仓储接口与业务规则。
//
// 依赖方向：transport → knowledge（本包）→ 标准库 / 驱动。
// 本包**不** import net/http，也不 import 任何 transport 包——领域不该知道 HTTP 的存在。
//
// 分层位置：handler 只做参数解析与响应渲染；业务规则（软删、分页上限、幂等）在 service；
// 本包负责"数据结构 + 持久化接口"，MySQL 实现见 mysql.go。
package knowledge

import (
	"errors"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound 表示目标不存在（或已被软删）。
//
// 为什么单独定义一个错误而不是直接返回 sql.ErrNoRows：
// 领域层不该让调用方知道"底层是 SQL"。传输层用 errors.Is(err, ErrNotFound) 映射成 404。
var ErrNotFound = errors.New("knowledge: 记录不存在")

// Collection 是一个知识库。
type Collection struct {
	ID          uuid.UUID
	Name        string
	Description string
	CreatedAt   time.Time  // UTC
	UpdatedAt   time.Time  // UTC
	DeletedAt   *time.Time // nil = 未删除
}

// Cursor 是 keyset 分页的游标。
//
// 为什么是复合游标：DATETIME(3) 只有毫秒精度，高吞吐下同一毫秒必然有多条；
// 只用 created_at 做游标会漏行/重复。所以用 (created_at, ID) 两个字段定位。
//
// 对「列出某知识库下的文档」，ID 承载的是 document_id（关联表没有独立 id 列）。
type Cursor struct {
	CreatedAt time.Time
	ID        uuid.UUID
}

// CollectionUpdate 是 PATCH 语义的更新参数：字段为 nil 表示"这一项不动"。
//
// 为什么用指针而不是空字符串：如果用一个 string 承载 name/description，
// "只想改名"的请求会把 description 一起清空——这是接口设计里最常见的一类 bug。
type CollectionUpdate struct {
	Name        *string
	Description *string
}
