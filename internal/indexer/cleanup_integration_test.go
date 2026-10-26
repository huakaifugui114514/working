//go:build integration

package indexer

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
)

const createTempVectorCleanupJobs = `
CREATE TEMPORARY TABLE vector_cleanup_jobs (
    document_id BINARY(16)    NOT NULL,
    status      VARCHAR(16)   NOT NULL,
    attempts    INT UNSIGNED  NOT NULL DEFAULT 0,
    available_at DATETIME(3)  NOT NULL,
    locked_at   DATETIME(3)       NULL,
    locked_by   VARCHAR(128)      NULL,
    last_error  VARCHAR(1024)     NULL,
    created_at  DATETIME(3)   NOT NULL,
    updated_at  DATETIME(3)   NOT NULL,
    PRIMARY KEY (document_id),
    KEY idx_vector_cleanup_claim (status, available_at),
    KEY idx_vector_cleanup_lease (status, locked_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci`

func newTestCleanupStore(t *testing.T) (*CleanupStore, *sql.Conn) {
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
	if _, err := conn.ExecContext(context.Background(), createTempVectorCleanupJobs); err != nil {
		t.Fatalf("创建临时 vector_cleanup_jobs: %v", err)
	}
	return NewCleanupStore(conn), conn
}

func insertCleanupJob(t *testing.T, conn *sql.Conn, id uuid.UUID, status string, available time.Time) {
	t.Helper()
	_, err := conn.ExecContext(context.Background(), `
		INSERT INTO vector_cleanup_jobs
		(document_id, status, attempts, available_at, created_at, updated_at)
		VALUES (?, ?, 0, ?, ?, ?)`,
		id[:], status, available, available, available)
	if err != nil {
		t.Fatalf("插入 cleanup job: %v", err)
	}
}

func TestCleanupStoreClaimComplete(t *testing.T) {
	store, conn := newTestCleanupStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	docID := uuid.New()
	insertCleanupJob(t, conn, docID, "pending", now.Add(-time.Second))

	job, err := store.Claim(ctx, "cleaner", now)
	if err != nil || job == nil || job.DocumentID != docID || job.Attempts != 1 {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if err := store.Complete(ctx, *job, now); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM vector_cleanup_jobs WHERE document_id=?`, docID[:]).Scan(&status); err != nil {
		t.Fatalf("查询: %v", err)
	}
	if status != "done" {
		t.Fatalf("status = %s", status)
	}
}

func TestCleanupStoreFailDeadAndRequeue(t *testing.T) {
	store, conn := newTestCleanupStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	docID := uuid.New()
	insertCleanupJob(t, conn, docID, "pending", now.Add(-time.Second))

	job, err := store.Claim(ctx, "cleaner", now)
	if err != nil || job == nil {
		t.Fatalf("Claim: %+v, %v", job, err)
	}
	if err := store.Fail(ctx, *job, "qdrant down", now.Add(time.Second), 1, now); err != nil {
		t.Fatalf("Fail: %v", err)
	}
	var status string
	if err := conn.QueryRowContext(ctx, `SELECT status FROM vector_cleanup_jobs WHERE document_id=?`, docID[:]).Scan(&status); err != nil {
		t.Fatalf("查询: %v", err)
	}
	if status != "dead" {
		t.Fatalf("status = %s, want dead", status)
	}

	other := uuid.New()
	insertCleanupJob(t, conn, other, "running", now.Add(-time.Hour))
	if _, err := conn.ExecContext(ctx, `UPDATE vector_cleanup_jobs SET locked_at=? WHERE document_id=?`, now.Add(-time.Hour), other[:]); err != nil {
		t.Fatalf("设置 locked_at: %v", err)
	}
	n, err := store.RequeueExpired(ctx, time.Minute, now)
	if err != nil || n != 1 {
		t.Fatalf("RequeueExpired = %d, %v", n, err)
	}
}
