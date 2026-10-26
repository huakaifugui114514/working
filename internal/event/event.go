// Package event 实现 Kafka 事件 outbox 和 dispatcher/consumer。
//
// 事实源仍是 MySQL：document_events 只保证“事件最终会被发布”，
// Kafka 消费失败不会丢正文/embedding job。
package event

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event 是一条待发布/已消费的领域事件。
type Event struct {
	ID         uuid.UUID       `json:"id"`
	Type       string          `json:"type"`
	DocumentID uuid.UUID       `json:"document_id"`
	Payload    json.RawMessage `json:"payload"`
	Attempts   int             `json:"attempts"`
}

// Store 是事件 outbox 的持久化接口。
type Store interface {
	RequeueExpired(ctx context.Context) (int64, error)
	Claim(ctx context.Context, limit int, lease time.Duration) ([]Event, error)
	MarkPublished(ctx context.Context, id uuid.UUID) error
	MarkFailed(ctx context.Context, ev Event, next time.Time, maxAttempts int) error
}

// Publisher 是 Kafka 生产者的最小接口，测试可替换。
type Publisher interface {
	Publish(ctx context.Context, ev Event) error
	Close() error
}
