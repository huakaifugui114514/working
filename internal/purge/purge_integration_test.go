//go:build integration

package purge

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
	"github.com/huakaifugui114514/working/internal/knowledge"
)

// 集成测试：需要本地 MySQL（deploy/docker-compose.yml）。
// 使用 TEMPORARY TABLE 隔离数据，不会碰真实表。
const createTempTables = `
CREATE TEMPORARY TABLE collections (
    id          BINARY(16)    NOT NULL,
    name        VARCHAR(255)  NOT NULL,
    description VARCHAR(1024)     NULL,
    created_at  DATETIME(3)   NOT NULL,
    updated_at  DATETIME(3)   NOT NULL,
    deleted_at  DATETIME(3)       NULL DEFAULT NULL,
    PRIMARY KEY (id),
    KEY idx_created_id (created_at, id),
    KEY idx_deleted_id (deleted_at, id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

CREATE TEMPORARY TABLE documents (
    id          BINARY(16)    NOT NULL,
    title       VARCHAR(512)  NOT NULL,
    source_uri  VARCHAR(2048)     NULL,
    mime_type   VARCHAR(128)      NULL,
    size_bytes  BIGINT UNSIGNED   NULL,
    created_at  DATETIME(3)   NOT NULL,
    updated_at  DATETIME(3)   NOT NULL,
    deleted_at  DATETIME(3)       NULL DEFAULT NULL,
    PRIMARY KEY (id),
    KEY idx_created_id (created_at, id),
    KEY idx_deleted_id (deleted_at, id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

CREATE TEMPORARY TABLE collection_documents (
    collection_id BINARY(16)  NOT NULL,
    document_id   BINARY(16)  NOT NULL,
    created_at    DATETIME(3) NOT NULL,
    PRIMARY KEY (collection_id, document_id),
    KEY idx_document (document_id),
    KEY idx_collection_created (collection_id, created_at, document_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

-- 临时表不能建 FULLTEXT 索引（MySQL 8 会报 1796）；
-- purge 只按 document_id 删除，不需要全文索引。
CREATE TEMPORARY TABLE document_contents (
    document_id     BINARY(16)    NOT NULL,
    content         LONGTEXT      NOT NULL,
    content_hash    BINARY(32)    NOT NULL,
    chunker_version INT UNSIGNED  NOT NULL,
    created_at      DATETIME(3)   NOT NULL,
    updated_at      DATETIME(3)   NOT NULL,
    PRIMARY KEY (document_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

CREATE TEMPORARY TABLE document_chunks (
    id          BINARY(16)    NOT NULL,
    document_id BINARY(16)    NOT NULL,
    chunk_index INT UNSIGNED  NOT NULL,
    content     TEXT          NOT NULL,
    char_count  INT UNSIGNED  NOT NULL,
    created_at  DATETIME(3)   NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_document_chunk (document_id, chunk_index)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

CREATE TEMPORARY TABLE embedding_jobs (
    document_id     BINARY(16)    NOT NULL,
    content_hash    BINARY(32)    NOT NULL,
    chunker_version INT UNSIGNED  NOT NULL,
    status          VARCHAR(16)   NOT NULL,
    attempts        INT UNSIGNED  NOT NULL DEFAULT 0,
    available_at    DATETIME(3)   NOT NULL,
    locked_at       DATETIME(3)       NULL,
    locked_by       VARCHAR(128)      NULL,
    last_error      VARCHAR(1024)     NULL,
    created_at      DATETIME(3)   NOT NULL,
    updated_at      DATETIME(3)   NOT NULL,
    PRIMARY KEY (document_id),
    KEY idx_embedding_jobs_claim (status, available_at),
    KEY idx_embedding_jobs_lease (status, locked_at)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;

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
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;`

func openIntegrationDB(t *testing.T) *sql.DB {
	t.Helper()

	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	db, err := database.Open(context.Background(), cfg.Database,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("连接本地 MySQL 失败（先 cd deploy && docker compose up -d）: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func newIntegrationDB(t *testing.T) *sql.Conn {
	t.Helper()

	db := openIntegrationDB(t)
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("获取固定连接失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	// multiStatements 在业务 DSN 上是关闭的，这里逐条执行。
	for _, stmt := range splitSQL(createTempTables) {
		if _, err := conn.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("创建临时表失败: %v\nSQL: %s", err, stmt)
		}
	}
	return conn
}

// splitSQL 按空行切分 DDL；测试 DDL 固定，不需要通用 SQL parser。
func splitSQL(raw string) []string {
	var stmts []string
	start := 0
	for i := 0; i < len(raw); i++ {
		if raw[i] == ';' {
			if stmt := raw[start : i+1]; len(stmt) > 0 {
				stmts = append(stmts, stmt)
			}
			start = i + 1
		}
	}
	return stmts
}

func insertCollection(t *testing.T, conn *sql.Conn, id uuid.UUID, name string, deletedAt *time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err := conn.ExecContext(context.Background(),
		`INSERT INTO collections (id, name, description, created_at, updated_at, deleted_at)
		 VALUES (?, ?, NULL, ?, ?, ?)`,
		id[:], name, now, now, deletedAt)
	if err != nil {
		t.Fatalf("插入 collection 失败: %v", err)
	}
}

func insertDocument(t *testing.T, conn *sql.Conn, id uuid.UUID, title string, deletedAt *time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err := conn.ExecContext(context.Background(),
		`INSERT INTO documents (id, title, source_uri, mime_type, size_bytes, created_at, updated_at, deleted_at)
		 VALUES (?, ?, NULL, NULL, NULL, ?, ?, ?)`,
		id[:], title, now, now, deletedAt)
	if err != nil {
		t.Fatalf("插入 document 失败: %v", err)
	}
}

func insertAssociation(t *testing.T, conn *sql.Conn, collectionID, documentID uuid.UUID) {
	t.Helper()
	_, err := conn.ExecContext(context.Background(),
		`INSERT INTO collection_documents (collection_id, document_id, created_at)
		 VALUES (?, ?, ?)`,
		collectionID[:], documentID[:], time.Now().UTC().Truncate(time.Millisecond))
	if err != nil {
		t.Fatalf("插入关联失败: %v", err)
	}
}

func insertContent(t *testing.T, conn *sql.Conn, documentID uuid.UUID, content string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	hash := sha256.Sum256([]byte(content))
	_, err := conn.ExecContext(context.Background(),
		`INSERT INTO document_contents
		 (document_id, content, content_hash, chunker_version, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		documentID[:], content, hash[:], knowledge.ContentChunkerVersion, now, now)
	if err != nil {
		t.Fatalf("插入 document_content 失败: %v", err)
	}
}

func insertChunk(t *testing.T, conn *sql.Conn, documentID uuid.UUID, index int, content string) {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("生成 chunk id 失败: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err = conn.ExecContext(context.Background(),
		`INSERT INTO document_chunks (id, document_id, chunk_index, content, char_count, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id[:], documentID[:], index, content, len([]rune(content)), now)
	if err != nil {
		t.Fatalf("插入 document_chunk 失败: %v", err)
	}
}

func insertJob(t *testing.T, conn *sql.Conn, documentID uuid.UUID, content string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	hash := sha256.Sum256([]byte(content))
	_, err := conn.ExecContext(context.Background(),
		`INSERT INTO embedding_jobs
		 (document_id, content_hash, chunker_version, status, attempts,
		  available_at, created_at, updated_at)
		 VALUES (?, ?, ?, 'pending', 0, ?, ?, ?)`,
		documentID[:], hash[:], knowledge.ContentChunkerVersion, now, now, now)
	if err != nil {
		t.Fatalf("插入 embedding_job 失败: %v", err)
	}
}

func countContentRows(t *testing.T, conn *sql.Conn, documentID uuid.UUID) (contents, chunks int) {
	t.Helper()
	if err := conn.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM document_contents WHERE document_id = ?`, documentID[:]).Scan(&contents); err != nil {
		t.Fatalf("统计 document_contents 失败: %v", err)
	}
	if err := conn.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM document_chunks WHERE document_id = ?`, documentID[:]).Scan(&chunks); err != nil {
		t.Fatalf("统计 document_chunks 失败: %v", err)
	}
	return contents, chunks
}

func countByID(t *testing.T, conn *sql.Conn, table string, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(context.Background(),
		"SELECT COUNT(*) FROM "+table+" WHERE id = ?", id[:]).Scan(&n); err != nil {
		t.Fatalf("统计 %s 失败: %v", table, err)
	}
	return n
}

func associationExists(t *testing.T, conn *sql.Conn, collectionID, documentID uuid.UUID) bool {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM collection_documents WHERE collection_id = ? AND document_id = ?`,
		collectionID[:], documentID[:]).Scan(&n); err != nil {
		t.Fatalf("查询关联失败: %v", err)
	}
	return n > 0
}

func TestIntegrationPurge(t *testing.T) {
	conn := newIntegrationDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	old := now.Add(-40 * 24 * time.Hour)
	recent := now.Add(-10 * 24 * time.Hour)

	var (
		cActive              = uuid.New()
		dActive              = uuid.New()
		cExpired             = uuid.New()
		dInExpiredCollection = uuid.New()
		dExpired             = uuid.New()
		cWithExpiredDoc      = uuid.New()
		cBoth                = uuid.New()
		dBoth                = uuid.New()
		dRecent              = uuid.New()
		cRecentDoc           = uuid.New()
		cRecent              = uuid.New()
		dRecentCollection    = uuid.New()
		dOrphan              = uuid.New()
		cOrphan              = uuid.New()
	)

	insertCollection(t, conn, cActive, "active", nil)
	insertDocument(t, conn, dActive, "active", nil)
	insertAssociation(t, conn, cActive, dActive)

	insertCollection(t, conn, cExpired, "expired", &old)
	insertDocument(t, conn, dInExpiredCollection, "in-expired", nil)
	insertAssociation(t, conn, cExpired, dInExpiredCollection)

	insertDocument(t, conn, dExpired, "expired", &old)
	insertCollection(t, conn, cWithExpiredDoc, "with-expired-doc", nil)
	insertAssociation(t, conn, cWithExpiredDoc, dExpired)

	insertCollection(t, conn, cBoth, "both-expired", &old)
	insertDocument(t, conn, dBoth, "both-expired", &old)
	insertAssociation(t, conn, cBoth, dBoth)

	insertDocument(t, conn, dRecent, "recent", &recent)
	insertCollection(t, conn, cRecentDoc, "recent-doc", nil)
	insertAssociation(t, conn, cRecentDoc, dRecent)

	insertCollection(t, conn, cRecent, "recent", &recent)
	insertDocument(t, conn, dRecentCollection, "recent-collection", nil)
	insertAssociation(t, conn, cRecent, dRecentCollection)

	insertDocument(t, conn, dOrphan, "orphan", &old)
	insertCollection(t, conn, cOrphan, "orphan", &old)

	runner := NewRunner(conn, logger)
	opts := Options{Retention: DefaultRetention, BatchSize: 2, Interval: 0}

	// dry-run 只统计，不改数据。
	dry, err := runner.Run(ctx, Options{
		Retention: opts.Retention,
		BatchSize: opts.BatchSize,
		Interval:  opts.Interval,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if !dry.DryRun || dry.Associations != 3 || dry.Documents != 3 || dry.Collections != 3 {
		t.Fatalf("dry-run stats = %+v, want 3/3/3", dry)
	}
	if countByID(t, conn, "documents", dOrphan) != 1 || countByID(t, conn, "collections", cOrphan) != 1 {
		t.Fatal("dry-run 不应删除数据")
	}

	stats, err := runner.Run(ctx, opts)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if stats.DryRun || stats.Associations != 3 || stats.Documents != 3 || stats.Collections != 3 {
		t.Fatalf("stats = %+v, want 3/3/3", stats)
	}
	if stats.Batches == 0 {
		t.Fatal("应至少执行一个批次")
	}

	for _, tc := range []struct {
		name  string
		table string
		id    uuid.UUID
	}{
		{"过期 document", "documents", dExpired},
		{"同时过期 document", "documents", dBoth},
		{"孤儿 document", "documents", dOrphan},
		{"过期 collection", "collections", cExpired},
		{"同时过期 collection", "collections", cBoth},
		{"孤儿 collection", "collections", cOrphan},
	} {
		if n := countByID(t, conn, tc.table, tc.id); n != 0 {
			t.Errorf("%s 应被物理删除, count=%d", tc.name, n)
		}
	}

	for _, tc := range []struct {
		name  string
		table string
		id    uuid.UUID
	}{
		{"active document", "documents", dActive},
		{"active collection", "collections", cActive},
		{"有活跃 collection 的过期 document 所在 collection", "collections", cWithExpiredDoc},
		{"过期 collection 中的活跃 document", "documents", dInExpiredCollection},
		{"保留期内 document", "documents", dRecent},
		{"保留期内 collection", "collections", cRecent},
		{"保留期内 collection 的活跃 document", "documents", dRecentCollection},
	} {
		if n := countByID(t, conn, tc.table, tc.id); n != 1 {
			t.Errorf("%s 应保留, count=%d", tc.name, n)
		}
	}

	// 过期父资源的关联必须删掉；活跃/保留期内的关联必须保留。
	if associationExists(t, conn, cActive, dActive) != true {
		t.Error("活跃关联不应被删除")
	}
	if associationExists(t, conn, cExpired, dInExpiredCollection) {
		t.Error("过期 collection 的关联应被删除")
	}
	if associationExists(t, conn, cWithExpiredDoc, dExpired) {
		t.Error("过期 document 的关联应被删除")
	}
	if associationExists(t, conn, cBoth, dBoth) {
		t.Error("双过期关联应被删除")
	}
	if !associationExists(t, conn, cRecentDoc, dRecent) {
		t.Error("保留期内 document 的关联应保留")
	}
	if !associationExists(t, conn, cRecent, dRecentCollection) {
		t.Error("保留期内 collection 的关联应保留")
	}

	// 再跑一次应当没有可清理的数据（幂等）。
	again, err := runner.Run(ctx, opts)
	if err != nil {
		t.Fatalf("第二次 purge: %v", err)
	}
	if again.Associations != 0 || again.Documents != 0 || again.Collections != 0 {
		t.Fatalf("第二次 purge 应为 0, got %+v", again)
	}
}

// TestIntegrationPurgeRespectsRestoreLock 验证 restore 与 purge 对同一父行的锁协议。
//
// 用 50 年保留期（cutoff 约 1976 年），把测试行伪装成 1970 年软删，
// 避免碰到本地库里其他真实数据。测试行按精确 id 清理。
func TestIntegrationPurgeDocumentContent(t *testing.T) {
	conn := newIntegrationDB(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	old := time.Now().UTC().Add(-2 * DefaultRetention).Truncate(time.Millisecond)
	expiredDoc := uuid.New()
	activeDoc := uuid.New()

	insertDocument(t, conn, expiredDoc, "expired-with-content", &old)
	insertContent(t, conn, expiredDoc, "过期正文")
	insertJob(t, conn, expiredDoc, "过期正文")
	insertChunk(t, conn, expiredDoc, 0, "过期第一段")
	insertChunk(t, conn, expiredDoc, 1, "过期第二段")

	insertDocument(t, conn, activeDoc, "active-with-content", nil)
	insertContent(t, conn, activeDoc, "活跃正文")
	insertJob(t, conn, activeDoc, "活跃正文")
	insertChunk(t, conn, activeDoc, 0, "活跃第一段")

	runner := NewRunner(conn, logger)
	opts := Options{Retention: DefaultRetention, BatchSize: 10, Interval: 0}

	// dry-run：只统计过期 document 的 content/chunks，不删数据。
	dry, err := runner.Run(ctx, Options{
		Retention: opts.Retention,
		BatchSize: opts.BatchSize,
		Interval:  opts.Interval,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	if dry.Documents != 1 || dry.Contents != 1 || dry.Chunks != 2 || dry.Jobs != 1 {
		t.Fatalf("dry-run stats = %+v, want documents=1 contents=1 chunks=2 jobs=1", dry)
	}
	if contents, chunks := countContentRows(t, conn, expiredDoc); contents != 1 || chunks != 2 {
		t.Fatalf("dry-run 不应删除数据: contents=%d chunks=%d", contents, chunks)
	}

	stats, err := runner.Run(ctx, opts)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if stats.Documents != 1 || stats.Contents != 1 || stats.Chunks != 2 || stats.Jobs != 1 {
		t.Fatalf("stats = %+v, want documents=1 contents=1 chunks=2 jobs=1", stats)
	}

	if n := countByID(t, conn, "documents", expiredDoc); n != 0 {
		t.Fatalf("过期 document 应被删除, count=%d", n)
	}
	if contents, chunks := countContentRows(t, conn, expiredDoc); contents != 0 || chunks != 0 {
		t.Fatalf("过期 document 的正文/chunks 应被删除: contents=%d chunks=%d", contents, chunks)
	}
	var expiredJobs int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM embedding_jobs WHERE document_id = ?`, expiredDoc[:],
	).Scan(&expiredJobs); err != nil {
		t.Fatalf("统计 embedding_jobs: %v", err)
	}
	if expiredJobs != 0 {
		t.Fatalf("过期 document 的 embedding job 应被删除, count=%d", expiredJobs)
	}
	var cleanupJobs int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM vector_cleanup_jobs WHERE document_id = ?`, expiredDoc[:],
	).Scan(&cleanupJobs); err != nil {
		t.Fatalf("统计 vector_cleanup_jobs: %v", err)
	}
	if cleanupJobs != 1 {
		t.Fatalf("过期 document 应留下 1 条 Qdrant 清理任务, count=%d", cleanupJobs)
	}

	// 活跃文档的正文/chunks 不能被误删。
	if n := countByID(t, conn, "documents", activeDoc); n != 1 {
		t.Fatalf("活跃 document 应保留, count=%d", n)
	}
	if contents, chunks := countContentRows(t, conn, activeDoc); contents != 1 || chunks != 1 {
		t.Fatalf("活跃 document 的正文/chunks 应保留: contents=%d chunks=%d", contents, chunks)
	}
}

func TestIntegrationPurgeRespectsRestoreLock(t *testing.T) {
	db := openIntegrationDB(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	runner := NewRunner(db, logger)

	now := time.Now().UTC().Truncate(time.Millisecond)
	old := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	collectionID := uuid.New()
	documentIDs := make([]uuid.UUID, 0, 2)

	t.Cleanup(func() {
		for _, id := range documentIDs {
			_, _ = db.ExecContext(context.Background(), `DELETE FROM documents WHERE id = ?`, id[:])
		}
		_, _ = db.ExecContext(context.Background(),
			`DELETE FROM collection_documents WHERE collection_id = ?`, collectionID[:])
		_, _ = db.ExecContext(context.Background(),
			`DELETE FROM collections WHERE id = ?`, collectionID[:])
	})

	if _, err := db.ExecContext(ctx,
		`INSERT INTO collections (id, name, description, created_at, updated_at, deleted_at)
		 VALUES (?, ?, NULL, ?, ?, NULL)`,
		collectionID[:], "purge-lock-collection", now, now); err != nil {
		t.Fatalf("插入 collection: %v", err)
	}

	insertExpiredDoc := func(id uuid.UUID, title string) {
		t.Helper()
		documentIDs = append(documentIDs, id)
		if _, err := db.ExecContext(ctx,
			`INSERT INTO documents (id, title, source_uri, mime_type, size_bytes, created_at, updated_at, deleted_at)
			 VALUES (?, ?, NULL, NULL, NULL, ?, ?, ?)`,
			id[:], title, now, now, old); err != nil {
			t.Fatalf("插入 document: %v", err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO collection_documents (collection_id, document_id, created_at)
			 VALUES (?, ?, ?)`,
			collectionID[:], id[:], now); err != nil {
			t.Fatalf("插入关联: %v", err)
		}
	}

	options := Options{
		Retention: 50 * 365 * 24 * time.Hour,
		BatchSize: 10,
		Interval:  0,
	}

	t.Run("restore 先锁则 purge 跳过", func(t *testing.T) {
		documentID := uuid.New()
		insertExpiredDoc(documentID, "purge-lock-restored")

		conn, err := db.Conn(ctx)
		if err != nil {
			t.Fatalf("获取连接: %v", err)
		}
		defer conn.Close()

		tx, err := conn.BeginTx(ctx, nil)
		if err != nil {
			t.Fatalf("开始 restore 事务: %v", err)
		}
		defer func() { _ = tx.Rollback() }()

		var locked sql.NullTime
		if err := tx.QueryRowContext(ctx,
			`SELECT deleted_at FROM documents WHERE id = ? FOR UPDATE`, documentID[:]).Scan(&locked); err != nil {
			t.Fatalf("锁 document: %v", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE documents SET deleted_at = NULL, updated_at = ? WHERE id = ?`, now, documentID[:]); err != nil {
			t.Fatalf("模拟 restore: %v", err)
		}

		done := make(chan Stats, 1)
		errCh := make(chan error, 1)
		go func() {
			stats, runErr := runner.Run(ctx, options)
			done <- stats
			errCh <- runErr
		}()

		// 给 purge 时间：它会选到该行，但在 FOR UPDATE 上等待 restore 提交。
		time.Sleep(200 * time.Millisecond)
		if err := tx.Commit(); err != nil {
			t.Fatalf("提交 restore: %v", err)
		}

		select {
		case runErr := <-errCh:
			if runErr != nil {
				t.Fatalf("purge: %v", runErr)
			}
			stats := <-done
			if stats.Documents != 0 || stats.Associations != 0 {
				t.Fatalf("restore 赢了却被 purge 删除: %+v", stats)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("purge 等待锁超时")
		}

		var deletedAt sql.NullTime
		if err := db.QueryRowContext(ctx,
			`SELECT deleted_at FROM documents WHERE id = ?`, documentID[:]).Scan(&deletedAt); err != nil {
			t.Fatalf("查询恢复后的 document: %v", err)
		}
		if deletedAt.Valid {
			t.Fatal("document 应已被 restore，deleted_at 应为 NULL")
		}
		var associations int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM collection_documents WHERE collection_id = ? AND document_id = ?`,
			collectionID[:], documentID[:]).Scan(&associations); err != nil {
			t.Fatalf("查询关联: %v", err)
		}
		if associations != 1 {
			t.Fatalf("restore 后关联应保留, count=%d", associations)
		}

		_, _ = db.ExecContext(ctx,
			`DELETE FROM collection_documents WHERE collection_id = ? AND document_id = ?`,
			collectionID[:], documentID[:])
		_, _ = db.ExecContext(ctx, `DELETE FROM documents WHERE id = ?`, documentID[:])
	})

	t.Run("purge 先删则 restore 404", func(t *testing.T) {
		documentID := uuid.New()
		insertExpiredDoc(documentID, "purge-lock-purged")

		stats, err := runner.Run(ctx, options)
		if err != nil {
			t.Fatalf("purge: %v", err)
		}
		if stats.Documents != 1 || stats.Associations != 1 {
			t.Fatalf("purge stats = %+v, want documents=1 associations=1", stats)
		}

		repo := knowledge.NewMySQLRepository(db)
		if _, err := repo.RestoreDocument(ctx, documentID); !errors.Is(err, knowledge.ErrNotFound) {
			t.Fatalf("物理删除后 restore 应返回 ErrNotFound, got %v", err)
		}
	})
}
