//go:build integration

package purge

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
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;`

func newIntegrationDB(t *testing.T) *sql.Conn {
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
