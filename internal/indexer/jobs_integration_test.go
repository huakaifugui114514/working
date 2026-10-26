//go:build integration

package indexer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
)

const createTempEmbeddingJobs = `
CREATE TEMPORARY TABLE embedding_jobs (
    document_id     BINARY(16)    NOT NULL,
    content_hash    BINARY(32)    NOT NULL,
    chunker_version INT UNSIGNED  NOT NULL,
    status          VARCHAR(16)   NOT NULL,
    attempts        INT UNSIGNED  NOT NULL DEFAULT 0,
    available_at    DATETIME(3)   NOT NULL,
    locked_at       DATETIME(3)   NULL,
    locked_by       VARCHAR(128)  NULL,
    last_error      VARCHAR(1024) NULL,
    created_at      DATETIME(3)   NOT NULL,
    updated_at      DATETIME(3)   NOT NULL,
    PRIMARY KEY (document_id),
    KEY idx_embedding_jobs_claim (status, available_at),
    KEY idx_embedding_jobs_lease (status, locked_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci`

func newTestJobStore(t *testing.T) (*JobStore, *sql.Conn) {
	t.Helper()
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("加载配置: %v", err)
	}
	db, err := database.Open(context.Background(), cfg.Database,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("连接 MySQL: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("获取连接: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := conn.ExecContext(context.Background(), createTempEmbeddingJobs); err != nil {
		t.Fatalf("创建临时 embedding_jobs: %v", err)
	}
	return NewJobStore(conn), conn
}

func insertJob(t *testing.T, conn *sql.Conn, id uuid.UUID, hash [32]byte, status string, available time.Time) {
	t.Helper()
	_, err := conn.ExecContext(context.Background(), `
		INSERT INTO embedding_jobs
		(document_id, content_hash, chunker_version, status, attempts,
		 available_at, created_at, updated_at)
		VALUES (?, ?, 1, ?, 0, ?, ?, ?)`,
		id[:], hash[:], status, available, available, available)
	if err != nil {
		t.Fatalf("插入 job: %v", err)
	}
}

func TestJobStoreClaimComplete(t *testing.T) {
	store, conn := newTestJobStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	docID := uuid.New()
	hash := sha256.Sum256([]byte("正文"))
	insertJob(t, conn, docID, hash, JobPending, now.Add(-time.Second))

	job, err := store.Claim(ctx, "worker-1", now)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if job == nil || job.DocumentID != docID || job.Attempts != 1 {
		t.Fatalf("job = %+v", job)
	}
	if job.ContentHash != hash {
		t.Fatalf("hash = %x", job.ContentHash)
	}

	// 只有一条任务，第二次抢不到。
	if second, err := store.Claim(ctx, "worker-2", now); err != nil || second != nil {
		t.Fatalf("second claim = %+v, err=%v", second, err)
	}

	if err := store.Complete(ctx, *job, now); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM embedding_jobs WHERE document_id=?`, docID[:]).Scan(&status); err != nil {
		t.Fatalf("查询 status: %v", err)
	}
	if status != JobDone {
		t.Fatalf("status = %s, want done", status)
	}
}

func TestJobStoreStaleComplete(t *testing.T) {
	store, conn := newTestJobStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	docID := uuid.New()
	hash := sha256.Sum256([]byte("旧正文"))
	newHash := sha256.Sum256([]byte("新正文"))
	insertJob(t, conn, docID, hash, JobPending, now.Add(-time.Second))

	job, err := store.Claim(ctx, "worker", now)
	if err != nil || job == nil {
		t.Fatalf("Claim: %+v, %v", job, err)
	}
	// 模拟正文又被替换：同一个 document_id 的 hash 变成新值。
	if _, err := conn.ExecContext(ctx, `UPDATE embedding_jobs SET content_hash=?, status='pending' WHERE document_id=?`,
		newHash[:], docID[:]); err != nil {
		t.Fatalf("更新新任务: %v", err)
	}
	if err := store.Complete(ctx, *job, now); !errors.Is(err, ErrJobStale) {
		t.Fatalf("Complete = %v, want ErrJobStale", err)
	}
	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM embedding_jobs WHERE document_id=?`, docID[:]).Scan(&status); err != nil {
		t.Fatalf("查询 status: %v", err)
	}
	if status != JobPending {
		t.Fatalf("新任务被旧 worker 覆盖: status=%s", status)
	}
}

func TestJobStoreFailAndDead(t *testing.T) {
	store, conn := newTestJobStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	docID := uuid.New()
	hash := sha256.Sum256([]byte("正文"))
	insertJob(t, conn, docID, hash, JobPending, now.Add(-time.Second))

	job, err := store.Claim(ctx, "worker", now)
	if err != nil || job == nil {
		t.Fatalf("Claim: %+v, %v", job, err)
	}
	if err := store.Fail(ctx, *job, "ollama down", now.Add(time.Second), 2, now); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	var status string
	var attempts int
	if err := conn.QueryRowContext(ctx, `SELECT status, attempts FROM embedding_jobs WHERE document_id=?`, docID[:]).Scan(&status, &attempts); err != nil {
		t.Fatalf("查询: %v", err)
	}
	if status != JobFailed || attempts != 1 {
		t.Fatalf("status/attempts = %s/%d", status, attempts)
	}

	// 到时间后再次抢占，第二次失败达到 maxAttempts -> dead。
	job2, err := store.Claim(ctx, "worker", now.Add(2*time.Second))
	if err != nil || job2 == nil {
		t.Fatalf("second Claim: %+v, %v", job2, err)
	}
	if err := store.Fail(ctx, *job2, "still down", now.Add(2*time.Second), 2, now.Add(2*time.Second)); err != nil {
		t.Fatalf("second Fail: %v", err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT status FROM embedding_jobs WHERE document_id=?`, docID[:]).Scan(&status); err != nil {
		t.Fatalf("查询: %v", err)
	}
	if status != JobDead {
		t.Fatalf("status = %s, want dead", status)
	}
}

func TestJobStoreRequeueExpired(t *testing.T) {
	store, conn := newTestJobStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	docID := uuid.New()
	hash := sha256.Sum256([]byte("正文"))
	insertJob(t, conn, docID, hash, JobRunning, now.Add(-time.Hour))
	if _, err := conn.ExecContext(ctx, `UPDATE embedding_jobs SET locked_at=? WHERE document_id=?`,
		now.Add(-time.Hour), docID[:]); err != nil {
		t.Fatalf("设置 locked_at: %v", err)
	}

	n, err := store.RequeueExpired(ctx, 5*time.Minute, now)
	if err != nil || n != 1 {
		t.Fatalf("RequeueExpired = %d, %v", n, err)
	}
	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM embedding_jobs WHERE document_id=?`, docID[:]).Scan(&status); err != nil {
		t.Fatalf("查询: %v", err)
	}
	if status != JobPending {
		t.Fatalf("status = %s, want pending", status)
	}
}
