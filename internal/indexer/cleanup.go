package indexer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

// CleanupJob 是一条 Qdrant 向量清理任务。
type CleanupJob struct {
	DocumentID uuid.UUID
	Attempts   int
}

// CleanupQueue 是 cleanup worker 依赖的任务队列接口。
type CleanupQueue interface {
	RequeueExpired(ctx context.Context, lease time.Duration, now time.Time) (int64, error)
	Claim(ctx context.Context, workerID string, now time.Time) (*CleanupJob, error)
	Complete(ctx context.Context, job CleanupJob, now time.Time) error
	Fail(ctx context.Context, job CleanupJob, cause string, retryAt time.Time, maxAttempts int, now time.Time) error
}

// CleanupStore 负责 vector_cleanup_jobs 的抢占、完成、失败与 lease 回收。
type CleanupStore struct {
	db DB
}

// NewCleanupStore 注入数据库连接。
func NewCleanupStore(db DB) *CleanupStore {
	if db == nil {
		panic("indexer.NewCleanupStore: db is nil")
	}
	return &CleanupStore{db: db}
}

// RequeueExpired 把 lease 过期的 running 任务退回 pending。
func (s *CleanupStore) RequeueExpired(ctx context.Context, lease time.Duration, now time.Time) (int64, error) {
	if lease <= 0 {
		return 0, fmt.Errorf("indexer: lease 必须为正数")
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE vector_cleanup_jobs
		SET status = 'pending', locked_at = NULL, locked_by = NULL, available_at = ?, updated_at = ?
		WHERE status = 'running' AND locked_at < ?`,
		now, now, now.Add(-lease))
	if err != nil {
		return 0, fmt.Errorf("indexer: 回收过期 vector cleanup 任务: %w", err)
	}
	return res.RowsAffected()
}

// Claim 抢占一条 pending/failed 且到期的清理任务。
func (s *CleanupStore) Claim(ctx context.Context, workerID string, now time.Time) (*CleanupJob, error) {
	if workerID == "" {
		return nil, fmt.Errorf("indexer: workerID 不能为空")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("indexer: begin cleanup claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		documentID []byte
		attempts   int
	)
	err = tx.QueryRowContext(ctx, `
		SELECT document_id, attempts
		FROM vector_cleanup_jobs
		WHERE status IN ('pending', 'failed') AND available_at <= ?
		ORDER BY available_at ASC
		LIMIT 1
		FOR UPDATE SKIP LOCKED`, now).Scan(&documentID, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("indexer: commit empty cleanup claim: %w", err)
		}
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("indexer: select vector cleanup job: %w", err)
	}
	docID, err := uuid.FromBytes(documentID)
	if err != nil {
		return nil, fmt.Errorf("indexer: parse cleanup document_id(%x): %w", documentID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE vector_cleanup_jobs
		SET status = 'running', locked_at = ?, locked_by = ?, attempts = attempts + 1, updated_at = ?
		WHERE document_id = ?`,
		now, workerID, now, documentID); err != nil {
		return nil, fmt.Errorf("indexer: mark cleanup job running: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("indexer: commit cleanup claim: %w", err)
	}
	return &CleanupJob{DocumentID: docID, Attempts: attempts + 1}, nil
}

// Complete 把清理任务标记为 done。
func (s *CleanupStore) Complete(ctx context.Context, job CleanupJob, now time.Time) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE vector_cleanup_jobs
		SET status = 'done', locked_at = NULL, locked_by = NULL, last_error = NULL, updated_at = ?
		WHERE document_id = ? AND status = 'running'`,
		now, job.DocumentID[:])
	if err != nil {
		return fmt.Errorf("indexer: complete vector cleanup: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("indexer: cleanup rows affected: %w", err)
	}
	if n == 0 {
		return ErrJobStale
	}
	return nil
}

// Fail 记录清理失败并按次数决定 failed/dead。
func (s *CleanupStore) Fail(
	ctx context.Context,
	job CleanupJob,
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
		UPDATE vector_cleanup_jobs
		SET status = ?, available_at = ?, locked_at = NULL, locked_by = NULL,
		    last_error = ?, updated_at = ?
		WHERE document_id = ? AND status = 'running'`,
		status, retryAt, truncateRunes(cause, 1000), now, job.DocumentID[:])
	if err != nil {
		return fmt.Errorf("indexer: fail vector cleanup: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("indexer: cleanup fail rows affected: %w", err)
	}
	if n == 0 {
		return ErrJobStale
	}
	return nil
}

// CleanupWorker 只负责删除 Qdrant 中某个 document 的全部向量。
type CleanupWorker struct {
	jobs        CleanupQueue
	vectors     VectorStore
	workerID    string
	lease       time.Duration
	maxAttempts int
	logger      *slog.Logger
}

// CleanupWorkerOptions 是 CleanupWorker 的依赖与参数。
type CleanupWorkerOptions struct {
	Jobs        CleanupQueue
	Vectors     VectorStore
	WorkerID    string
	Lease       time.Duration
	MaxAttempts int
	Logger      *slog.Logger
}

// NewCleanupWorker 校验依赖并构造 worker。
func NewCleanupWorker(opts CleanupWorkerOptions) (*CleanupWorker, error) {
	if opts.Jobs == nil {
		return nil, fmt.Errorf("indexer: cleanup jobs is nil")
	}
	if opts.Vectors == nil {
		return nil, fmt.Errorf("indexer: cleanup vectors is nil")
	}
	if opts.WorkerID == "" {
		return nil, fmt.Errorf("indexer: cleanup workerID 不能为空")
	}
	if opts.Lease <= 0 {
		return nil, fmt.Errorf("indexer: cleanup lease 必须为正数")
	}
	if opts.MaxAttempts < 1 {
		return nil, fmt.Errorf("indexer: cleanup maxAttempts 必须为正数")
	}
	if opts.Logger == nil {
		return nil, fmt.Errorf("indexer: cleanup logger is nil")
	}
	return &CleanupWorker{
		jobs:        opts.Jobs,
		vectors:     opts.Vectors,
		workerID:    opts.WorkerID,
		lease:       opts.Lease,
		maxAttempts: opts.MaxAttempts,
		logger:      opts.Logger,
	}, nil
}

// RunOnce 处理一条清理任务。false 表示当前没有任务。
func (w *CleanupWorker) RunOnce(ctx context.Context) (bool, error) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	requeued, err := w.jobs.RequeueExpired(ctx, w.lease, now)
	if err != nil {
		return false, err
	}
	if requeued > 0 {
		w.logger.Warn("回收过期 vector cleanup 任务", "count", requeued)
	}
	job, err := w.jobs.Claim(ctx, w.workerID, now)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}

	w.logger.Info("开始清理 Qdrant 向量", "document_id", job.DocumentID, "attempts", job.Attempts)
	if err := w.vectors.DeleteByDocument(ctx, job.DocumentID); err != nil {
		failErr := w.jobs.Fail(ctx, *job, err.Error(), now.Add(backoff(job.Attempts)), w.maxAttempts, now)
		if failErr != nil && !errors.Is(failErr, ErrJobStale) {
			w.logger.Error("记录 vector cleanup 失败状态失败", "document_id", job.DocumentID, "error", failErr)
		}
		return true, fmt.Errorf("cleanup document %s: %w", job.DocumentID, err)
	}
	if err := w.jobs.Complete(ctx, *job, now); err != nil && !errors.Is(err, ErrJobStale) {
		return true, fmt.Errorf("complete cleanup document %s: %w", job.DocumentID, err)
	}
	w.logger.Info("Qdrant 向量清理完成", "document_id", job.DocumentID)
	return true, nil
}

// Run 循环处理清理任务。
func (w *CleanupWorker) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("indexer: cleanup interval 必须为正数")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		for {
			processed, err := w.RunOnce(ctx)
			if err != nil {
				w.logger.Error("vector cleanup 任务失败", "error", err)
			}
			if !processed {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
