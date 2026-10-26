//go:build integration

package knowledge

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
)

// 检索集成测试必须用真实表：MySQL 不允许在 TEMPORARY TABLE 上建 FULLTEXT
// （错误 1796）。所以这里直接连真实库，靠独立 collection_id 隔离数据，
// 测试结束按 id 清理。
func TestIntegrationSearch(t *testing.T) {
	db := newSearchIntegrationDB(t)
	repo := NewMySQLRepository(db)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Millisecond)
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	token := "kfsearch" + suffix

	var (
		c1 = uuid.New()
		c2 = uuid.New()
		d1 = uuid.New()
		d2 = uuid.New()
		d3 = uuid.New()
	)

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = db.ExecContext(cleanupCtx,
			`DELETE FROM document_chunks WHERE document_id IN (?, ?, ?)`, d1[:], d2[:], d3[:])
		_, _ = db.ExecContext(cleanupCtx,
			`DELETE FROM collection_documents WHERE collection_id IN (?, ?)`, c1[:], c2[:])
		_, _ = db.ExecContext(cleanupCtx,
			`DELETE FROM documents WHERE id IN (?, ?, ?)`, d1[:], d2[:], d3[:])
		_, _ = db.ExecContext(cleanupCtx,
			`DELETE FROM collections WHERE id IN (?, ?)`, c1[:], c2[:])
	})

	insertSearchCollection(t, db, c1, "search-test-"+suffix+"-1", nil, now)
	insertSearchCollection(t, db, c2, "search-test-"+suffix+"-2", nil, now)
	insertSearchDocument(t, db, d1, "文档一", nil, now)
	insertSearchDocument(t, db, d2, "文档二", nil, now)
	insertSearchDocument(t, db, d3, "文档三", nil, now)
	insertSearchAssociation(t, db, c1, d1, now)
	insertSearchAssociation(t, db, c1, d2, now)
	insertSearchAssociation(t, db, c2, d3, now)
	insertSearchChunk(t, db, d1, 0, "甲 "+token+" 企业知识库支持全文检索")
	insertSearchChunk(t, db, d2, 0, "乙 "+token+" 普通文档")
	insertSearchChunk(t, db, d3, 0, "丙 "+token+" 另一个知识库")

	t.Run("collection 过滤只返回该 collection 的 chunk", func(t *testing.T) {
		hits, hasMore, err := repo.Search(ctx, token, &c1, 10, 0)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if hasMore {
			t.Fatal("10 条上限下不应有下一页")
		}
		assertHitDocuments(t, hits, map[uuid.UUID]bool{d1: true, d2: true})
	})

	t.Run("全局检索返回所有 collection 的 chunk", func(t *testing.T) {
		hits, hasMore, err := repo.Search(ctx, token, nil, 10, 0)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if hasMore {
			t.Fatal("10 条上限下不应有下一页")
		}
		assertHitDocuments(t, hits, map[uuid.UUID]bool{d1: true, d2: true, d3: true})
	})

	t.Run("中文查询命中", func(t *testing.T) {
		hits, _, err := repo.Search(ctx, "知识库", &c1, 10, 0)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		assertHitDocuments(t, hits, map[uuid.UUID]bool{d1: true})
	})

	t.Run("limit+1 正确判断 hasMore", func(t *testing.T) {
		hits, hasMore, err := repo.Search(ctx, token, &c1, 1, 0)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(hits) != 1 || !hasMore {
			t.Fatalf("limit=1 第一页 = %d 条, hasMore=%v, want 1/true", len(hits), hasMore)
		}

		hits, hasMore, err = repo.Search(ctx, token, &c1, 1, 1)
		if err != nil {
			t.Fatalf("Search offset=1: %v", err)
		}
		if len(hits) != 1 || hasMore {
			t.Fatalf("limit=1 offset=1 = %d 条, hasMore=%v, want 1/false", len(hits), hasMore)
		}
	})

	t.Run("软删文档不会出现在结果里", func(t *testing.T) {
		if _, err := db.ExecContext(ctx,
			`UPDATE documents SET deleted_at = ?, updated_at = ? WHERE id = ?`,
			now, now, d2[:]); err != nil {
			t.Fatalf("软删 d2: %v", err)
		}

		hits, _, err := repo.Search(ctx, token, &c1, 10, 0)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		assertHitDocuments(t, hits, map[uuid.UUID]bool{d1: true})
	})

	t.Run("软删 collection 不会返回它的 chunk", func(t *testing.T) {
		if _, err := db.ExecContext(ctx,
			`UPDATE collections SET deleted_at = ?, updated_at = ? WHERE id = ?`,
			now, now, c2[:]); err != nil {
			t.Fatalf("软删 c2: %v", err)
		}

		hits, _, err := repo.Search(ctx, token, &c2, 10, 0)
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(hits) != 0 {
			t.Fatalf("软删 collection 应返回 0 条, got %d", len(hits))
		}
	})
}

func newSearchIntegrationDB(t *testing.T) *sql.DB {
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

func insertSearchCollection(t *testing.T, db *sql.DB, id uuid.UUID, name string, deletedAt *time.Time, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO collections (id, name, description, created_at, updated_at, deleted_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id[:], name, "search integration test", now, now, deletedAt)
	if err != nil {
		t.Fatalf("插入 search collection 失败: %v", err)
	}
}

func insertSearchDocument(t *testing.T, db *sql.DB, id uuid.UUID, title string, deletedAt *time.Time, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO documents (id, title, source_uri, mime_type, size_bytes, created_at, updated_at, deleted_at)
		 VALUES (?, ?, NULL, NULL, NULL, ?, ?, ?)`,
		id[:], title, now, now, deletedAt)
	if err != nil {
		t.Fatalf("插入 search document 失败: %v", err)
	}
}

func insertSearchAssociation(t *testing.T, db *sql.DB, collectionID, documentID uuid.UUID, now time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO collection_documents (collection_id, document_id, created_at)
		 VALUES (?, ?, ?)`,
		collectionID[:], documentID[:], now)
	if err != nil {
		t.Fatalf("插入 search association 失败: %v", err)
	}
}

func insertSearchChunk(t *testing.T, db *sql.DB, documentID uuid.UUID, index int, content string) {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("生成 chunk id 失败: %v", err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err = db.ExecContext(context.Background(),
		`INSERT INTO document_chunks (id, document_id, chunk_index, content, char_count, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id[:], documentID[:], index, content, utf8.RuneCountInString(content), now)
	if err != nil {
		t.Fatalf("插入 search chunk 失败: %v", err)
	}
}

func assertHitDocuments(t *testing.T, hits []SearchHit, want map[uuid.UUID]bool) {
	t.Helper()

	got := make(map[uuid.UUID]bool, len(hits))
	for _, hit := range hits {
		if hit.Score <= 0 {
			t.Errorf("chunk %s 的 score = %f, want > 0", hit.ChunkID, hit.Score)
		}
		if hit.ChunkID == uuid.Nil || hit.DocumentID == uuid.Nil {
			t.Errorf("命中结果缺少 chunk/document id: %+v", hit)
		}
		if hit.DocumentTitle == "" || hit.Content == "" {
			t.Errorf("命中结果缺少 title/content: %+v", hit)
		}
		got[hit.DocumentID] = true
	}

	if len(got) != len(want) {
		t.Fatalf("命中 document 数 = %d, want %d; got=%v", len(got), len(want), got)
	}
	for id := range want {
		if !got[id] {
			t.Errorf("缺少 document %s 的命中; got=%v", id, got)
		}
	}
}
