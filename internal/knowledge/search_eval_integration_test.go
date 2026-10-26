//go:build integration

package knowledge

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/eval"
)

// TestSearchEvalBaseline 用固定评测集跑当前 MySQL FULLTEXT 基线。
//
// 它不设“必须达到多少分”的硬阈值：这是测量，不是功能正确性测试。
// 输出用于之后 vector / hybrid 的对比。
func TestSearchEvalBaseline(t *testing.T) {
	set, err := eval.Load(filepath.Join("..", "eval", "testdata", "search_eval.json"))
	if err != nil {
		t.Fatalf("加载评测集: %v", err)
	}

	db := newSearchIntegrationDB(t)
	repo := NewMySQLRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	suffix := uuid.NewString()[:8]

	collectionID := uuid.New()
	docIDs := make(map[string]uuid.UUID, len(set.Documents))
	for _, doc := range set.Documents {
		docIDs[doc.ID] = uuid.New()
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		for _, id := range docIDs {
			_, _ = db.ExecContext(cleanupCtx, `DELETE FROM document_chunks WHERE document_id = ?`, id[:])
			_, _ = db.ExecContext(cleanupCtx, `DELETE FROM documents WHERE id = ?`, id[:])
		}
		_, _ = db.ExecContext(cleanupCtx,
			`DELETE FROM collection_documents WHERE collection_id = ?`, collectionID[:])
		_, _ = db.ExecContext(cleanupCtx,
			`DELETE FROM collections WHERE id = ?`, collectionID[:])
	})

	insertSearchCollection(t, db, collectionID, "search-eval-"+suffix, nil, now)
	for _, doc := range set.Documents {
		id := docIDs[doc.ID]
		insertSearchDocument(t, db, id, doc.Title, nil, now)
		insertSearchAssociation(t, db, collectionID, id, now)
		insertSearchChunk(t, db, id, 0, doc.Content)
	}

	found := make([][]string, 0, len(set.Queries))
	for _, q := range set.Queries {
		hits, _, err := repo.Search(ctx, q.Query, &collectionID, 5, 0)
		if err != nil {
			t.Fatalf("query %q: %v", q.Query, err)
		}

		ranked := make([]string, 0, len(hits))
		seen := make(map[uuid.UUID]bool, len(hits))
		for _, hit := range hits {
			if seen[hit.DocumentID] {
				continue
			}
			seen[hit.DocumentID] = true
			for key, id := range docIDs {
				if id == hit.DocumentID {
					ranked = append(ranked, key)
					break
				}
			}
		}
		found = append(found, ranked)
		t.Logf("%-10s query=%-28q got=%v expected=%v", q.Type, q.Query, ranked, q.Expected)
	}

	metrics := set.Evaluate(found)
	t.Logf("BASELINE MySQL FULLTEXT: queries=%d hits=%d recall@5=%.3f mrr=%.3f no_results=%d",
		metrics.Queries, metrics.Hits, metrics.RecallAt5, metrics.MRR, metrics.NoResults)

	if metrics.Queries != len(set.Queries) {
		t.Fatalf("评测 query 数 = %d, want %d", metrics.Queries, len(set.Queries))
	}
}
