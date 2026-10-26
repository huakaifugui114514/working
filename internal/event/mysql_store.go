package event

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// MySQLStore 用 MySQL 行锁实现事件抢占。
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore 注入连接池。
func NewMySQLStore(db *sql.DB) *MySQLStore {
	if db == nil {
		panic("event.NewMySQLStore: db is nil")
	}
	return &MySQLStore{db: db}
}

// RequeueExpired 回收 dispatcher 崩溃留下的 publishing 事件。
func (s *MySQLStore) RequeueExpired(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE document_events
		SET status='pending', available_at=UTC_TIMESTAMP(3)
		WHERE status='publishing' AND available_at<=UTC_TIMESTAMP(3)`)
	if err != nil {
		return 0, fmt.Errorf("event: requeue expired: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// Claim 抢占一批 pending 事件，并写入 publishing + lease。
func (s *MySQLStore) Claim(ctx context.Context, limit int, lease time.Duration) ([]Event, error) {
	if limit < 1 {
		return nil, fmt.Errorf("event: limit 必须 ≥ 1")
	}
	if lease <= 0 {
		return nil, fmt.Errorf("event: lease 必须为正数")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("event: begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx, `
		SELECT id, event_type, document_id, payload, attempts
		FROM document_events
		WHERE status='pending' AND available_at<=UTC_TIMESTAMP(3)
		ORDER BY id
		LIMIT ?
		FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return nil, fmt.Errorf("event: select claim: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []Event
	for rows.Next() {
		var (
			ev      Event
			idRaw   []byte
			docRaw  []byte
			payload []byte
		)
		if err := rows.Scan(&idRaw, &ev.Type, &docRaw, &payload, &ev.Attempts); err != nil {
			return nil, fmt.Errorf("event: scan claim: %w", err)
		}
		id, err := uuid.FromBytes(idRaw)
		if err != nil {
			return nil, fmt.Errorf("event: parse event id: %w", err)
		}
		docID, err := uuid.FromBytes(docRaw)
		if err != nil {
			return nil, fmt.Errorf("event: parse document id: %w", err)
		}
		ev.ID = id
		ev.DocumentID = docID
		ev.Payload = payload
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("event: iterate claim: %w", err)
	}
	if len(events) == 0 {
		return nil, nil
	}

	placeholders := make([]string, 0, len(events))
	args := make([]any, 0, len(events)+2)
	args = append(args, lease.Microseconds())
	for _, ev := range events {
		placeholders = append(placeholders, "?")
		args = append(args, ev.ID[:])
	}
	query := `UPDATE document_events
		SET status='publishing', attempts=attempts+1,
		    available_at=DATE_ADD(UTC_TIMESTAMP(3), INTERVAL ? MICROSECOND)
		WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	if _, err := tx.ExecContext(ctx, query, args...); err != nil {
		return nil, fmt.Errorf("event: mark publishing: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("event: commit claim: %w", err)
	}
	for i := range events {
		events[i].Attempts++
	}
	return events, nil
}

// MarkPublished 标记 Kafka 已确认。
func (s *MySQLStore) MarkPublished(ctx context.Context, id uuid.UUID) error {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE document_events
		SET status='published', published_at=UTC_TIMESTAMP(3)
		WHERE id=? AND status='publishing'`, id[:]); err != nil {
		return fmt.Errorf("event: mark published: %w", err)
	}
	return nil
}

// MarkFailed 安排重试；attempts 由 Claim 递增，调用方用 ev.Attempts 判断是否 dead。
func (s *MySQLStore) MarkFailed(ctx context.Context, ev Event, next time.Time, maxAttempts int) error {
	if maxAttempts < 1 {
		maxAttempts = 5
	}
	status := "pending"
	if ev.Attempts >= maxAttempts {
		status = "dead"
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE document_events
		SET status=?, available_at=?
		WHERE id=? AND status='publishing'`, status, next.UTC(), ev.ID[:]); err != nil {
		return fmt.Errorf("event: mark failed: %w", err)
	}
	return nil
}
