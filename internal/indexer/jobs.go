// Package indexer 把 MySQL 里的 chunk 写成 Qdrant 向量。
//
// 职责边界：任务抢占、正文/分块读取、调用 Embedder、写 Qdrant。
// 它不处理 HTTP，也不改变正文；正文变更只负责在事务里写 embedding_jobs。
package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const (
	JobPending = "pending"
	JobRunning = "running"
	JobDone    = "done"
	JobFailed  = "failed"
	JobDead    = "dead"
)

// ErrJobStale 表示任务在执行期间被新的正文替换覆盖，不能再把旧结果标成 done。
var ErrJobStale = errors.New("indexer: embedding job 已被更新的正文覆盖")

// Job 是一条被抢占的 embedding 任务。
type Job struct {
	DocumentID     uuid.UUID
	ContentHash    [32]byte
	ChunkerVersion int
	Attempts       int
}

// JobQueue 是 worker 依赖的任务队列接口。
//
// 用接口而不是直接依赖 *JobStore：worker 的单测可以 fake，不需要真实 MySQL。
type JobQueue interface {
	RequeueExpired(ctx context.Context, lease time.Duration, now time.Time) (int64, error)
	Claim(ctx context.Context, workerID string, now time.Time) (*Job, error)
	Complete(ctx context.Context, job Job, now time.Time) error
	Fail(ctx context.Context, job Job, cause string, retryAt time.Time, maxAttempts int, now time.Time) error
}

// DB 是 *sql.DB / *sql.Conn 的公共子集，方便集成测试用临时表。
type DB interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// JobStore 负责 embedding_jobs 的抢占、完成、失败与 lease 回收。
type JobStore struct {
	db DB
}

// NewJobStore 注入数据库连接。
func NewJobStore(db DB) *JobStore {
	if db == nil {
		panic("indexer.NewJobStore: db is nil")
	}
	return &JobStore{db: db}
}

// RequeueExpired 把 lease 过期的 running 任务退回 pending。
//
// worker 崩溃后不会永远占着任务；下一次启动/每轮都会调用。
func (s *JobStore) RequeueExpired(ctx context.Context, lease time.Duration, now time.Time) (int64, error) {
	if lease <= 0 {
		return 0, fmt.Errorf("indexer: lease 必须为正数")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE embedding_jobs
		SET status = 'pending',
		    locked_at = NULL,
		    locked_by = NULL,
		    available_at = ?,
		    updated_at = ?
		WHERE status = 'running' AND locked_at < ?`,
		now, now, now.Add(-lease))
	if err != nil {
		return 0, fmt.Errorf("indexer: 回收过期 embedding 任务: %w", err)
	}
	return res.RowsAffected()
}

// Claim 抢占一条 pending/failed 且到期的任务。
//
// 返回 nil, nil 表示当前没有可执行任务。
// FOR UPDATE SKIP LOCKED 让多个 worker 不会抢同一条。
func (s *JobStore) Claim(ctx context.Context, workerID string, now time.Time) (*Job, error) {
	if workerID == "" {
		return nil, fmt.Errorf("indexer: workerID 不能为空")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("indexer: begin claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		documentID []byte
		hash       []byte
		version    int
		attempts   int
	)
	err = tx.QueryRowContext(ctx, `
		SELECT document_id, content_hash, chunker_version, attempts
		FROM embedding_jobs
		WHERE status IN ('pending', 'failed') AND available_at <= ?
		ORDER BY available_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED`, now).Scan(&documentID, &hash, &version, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("indexer: commit empty claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("indexer: select embedding job: %w", err)
	}

	docUUID, err := uuid.FromBytes(documentID)
	if err != nil {
		return nil, fmt.Errorf("indexer: parse document_id(%x): %w", documentID, err)
	}
	if len(hash) != 32 {
		return nil, fmt.Errorf("indexer: content_hash 长度=%d, want 32", len(hash))
	}
	var contentHash [32]byte
	copy(contentHash[:], hash)

	if _, err := tx.ExecContext(ctx, `
		UPDATE embedding_jobs
		SET status = 'running', locked_at = ?, locked_by = ?, attempts = attempts + 1, updated_at = ?
		WHERE document_id = ? AND content_hash = ?`,
		now, workerID, now, documentID, hash); err != nil {
		return nil, fmt.Errorf("indexer: mark job running: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("indexer: commit claim: %w", err)
	}
	return &Job{
		DocumentID:     docUUID,
		ContentHash:    contentHash,
		ChunkerVersion: version,
		Attempts:       attempts + 1,
	}, nil
}

// Complete 把任务标记为 done。
//
// WHERE 带 content_hash：如果执行期间正文又变了，新任务可能已经把 hash 改掉，
// 这时 RowsAffected=0，返回 ErrJobStale，不能覆盖新任务。
func (s *JobStore) Complete(ctx context.Context, job Job, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE embedding_jobs
		SET status = 'done', locked_at = NULL, locked_by = NULL, last_error = NULL, updated_at = ?
		WHERE document_id = ? AND content_hash = ? AND status = 'running'`,
		now, job.DocumentID[:], job.ContentHash[:])
	if err != nil {
		return fmt.Errorf("indexer: complete embedding job: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("indexer: complete rows affected: %w", err)
	}
	if n == 0 {
		return ErrJobStale
	}
	return nil
}

// Fail 记录失败并按重试次数决定 failed 还是 dead。
//
// 返回 ErrJobStale 表示任务已被新正文覆盖，不能再改它的状态。
func (s *JobStore) Fail(
	ctx context.Context,
	job Job,
	cause string,
	retryAt time.Time,
	maxAttempts int,
	now time.Time,
) error {
	if maxAttempts < 1 {
		return fmt.Errorf("indexer: maxAttempts 必须为正数")
	}
	status := JobFailed
	if job.Attempts >= maxAttempts {
		status = JobDead
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE embedding_jobs
		SET status = ?, available_at = ?, locked_at = NULL, locked_by = NULL,
		    last_error = ?, updated_at = ?
		WHERE document_id = ? AND content_hash = ? AND status = 'running'`,
		status, retryAt, truncateRunes(cause, 1000), now,
		job.DocumentID[:], job.ContentHash[:])
	if err != nil {
		return fmt.Errorf("indexer: fail embedding job: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("indexer: fail rows affected: %w", err)
	}
	if n == 0 {
		return ErrJobStale
	}
	return nil
}

func truncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}
