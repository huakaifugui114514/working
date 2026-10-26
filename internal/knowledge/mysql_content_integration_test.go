//go:build integration

package knowledge

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func TestIntegrationReplaceDocumentContent(t *testing.T) {
	repo, conn := newIntegrationRepo(t)
	ctx := context.Background()

	doc := mustCreateDocument(t, repo, "正文替换测试")

	content1 := "第一段第二段第三段"
	hash1 := sha256.Sum256([]byte(content1))
	chunks1 := makeChunks("第一段", "第二段", "第三段")
	if err := repo.ReplaceDocumentContent(ctx, doc.ID, content1, hash1, chunks1); err != nil {
		t.Fatalf("ReplaceDocumentContent: %v", err)
	}

	got, err := repo.GetDocumentContent(ctx, doc.ID)
	if err != nil {
		t.Fatalf("GetDocumentContent: %v", err)
	}
	if got.DocumentID != doc.ID {
		t.Fatalf("DocumentID = %v, want %v", got.DocumentID, doc.ID)
	}
	if got.Content != content1 || got.ContentHash != hash1 {
		t.Fatalf("正文/hash 不一致: content=%q hash=%x", got.Content, got.ContentHash)
	}
	if got.ChunkerVersion != ContentChunkerVersion {
		t.Fatalf("ChunkerVersion = %d, want %d", got.ChunkerVersion, ContentChunkerVersion)
	}
	if got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("时间不应为零值: created=%v updated=%v", got.CreatedAt, got.UpdatedAt)
	}
	if got.CreatedAt.After(got.UpdatedAt) {
		t.Fatalf("created_at 晚于 updated_at: %v > %v", got.CreatedAt, got.UpdatedAt)
	}

	assertChunkRows(t, conn, doc.ID, []string{"第一段", "第二段", "第三段"})
	assertEmbeddingJob(t, conn, doc.ID, hash1, "pending", 0)
	assertDocumentEvent(t, conn, doc.ID, hash1)

	stored, err := repo.ListDocumentChunks(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListDocumentChunks: %v", err)
	}
	if len(stored) != 3 || stored[0].Index != 0 || stored[2].Index != 2 || stored[1].Content != "第二段" {
		t.Fatalf("stored chunks = %+v", stored)
	}

	// 第二次替换只用 2 个 chunk：旧的第 3 个 chunk 必须被删掉，
	// 不能留下上一代的尾巴。
	time.Sleep(10 * time.Millisecond)
	content2 := "新第一段新第二段"
	hash2 := sha256.Sum256([]byte(content2))
	chunks2 := makeChunks("新第一段", "新第二段")
	if err := repo.ReplaceDocumentContent(ctx, doc.ID, content2, hash2, chunks2); err != nil {
		t.Fatalf("第二次 ReplaceDocumentContent: %v", err)
	}

	got2, err := repo.GetDocumentContent(ctx, doc.ID)
	if err != nil {
		t.Fatalf("第二次 GetDocumentContent: %v", err)
	}
	if got2.Content != content2 || got2.ContentHash != hash2 {
		t.Fatalf("第二次正文/hash 不一致: content=%q hash=%x", got2.Content, got2.ContentHash)
	}
	if !got2.CreatedAt.Equal(got.CreatedAt) {
		t.Fatalf("upsert 不应改 created_at: old=%v new=%v", got.CreatedAt, got2.CreatedAt)
	}
	if !got2.UpdatedAt.After(got.UpdatedAt) {
		t.Fatalf("upsert 应刷新 updated_at: old=%v new=%v", got.UpdatedAt, got2.UpdatedAt)
	}
	if got2.ChunkerVersion != ContentChunkerVersion {
		t.Fatalf("第二次 ChunkerVersion = %d, want %d", got2.ChunkerVersion, ContentChunkerVersion)
	}

	assertChunkRows(t, conn, doc.ID, []string{"新第一段", "新第二段"})
	// 第二次替换应把同一个 job 重置成 pending + 新 hash，不能留下旧任务。
	assertEmbeddingJob(t, conn, doc.ID, hash2, "pending", 0)

	// document_contents 的主键是 document_id，upsert 后必须仍然只有一行。
	var contentRows int
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM document_contents WHERE document_id = ?`, doc.ID[:],
	).Scan(&contentRows); err != nil {
		t.Fatalf("统计 document_contents: %v", err)
	}
	if contentRows != 1 {
		t.Fatalf("document_contents 行数 = %d, want 1", contentRows)
	}
}

func TestIntegrationDocumentContentSoftDeleteAndRestore(t *testing.T) {
	repo, conn := newIntegrationRepo(t)
	ctx := context.Background()

	doc := mustCreateDocument(t, repo, "正文软删测试")
	content := "软删后仍然保留的正文"
	hash := sha256.Sum256([]byte(content))
	chunks := makeChunks("软删后仍然保留的第一段", "软删后仍然保留的第二段")

	if err := repo.ReplaceDocumentContent(ctx, doc.ID, content, hash, chunks); err != nil {
		t.Fatalf("ReplaceDocumentContent: %v", err)
	}

	if err := repo.SoftDeleteDocument(ctx, doc.ID); err != nil {
		t.Fatalf("SoftDeleteDocument: %v", err)
	}

	if _, err := repo.GetDocumentContent(ctx, doc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("软删后 GetDocumentContent 应返回 ErrNotFound, got %v", err)
	}
	if chunks, err := repo.ListDocumentChunks(ctx, doc.ID); err != nil || len(chunks) != 0 {
		t.Fatalf("软删后 ListDocumentChunks = %v, %v; want empty", chunks, err)
	}

	// 文档已软删时不能写正文；这一步必须在锁父行后返回 ErrNotFound，
	// 不能先删掉旧 chunks 再失败。
	if err := repo.ReplaceDocumentContent(ctx, doc.ID, "不应写入", sha256.Sum256([]byte("不应写入")), makeChunks("不应写入")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("软删文档 ReplaceDocumentContent 应返回 ErrNotFound, got %v", err)
	}
	assertChunkRows(t, conn, doc.ID, []string{"软删后仍然保留的第一段", "软删后仍然保留的第二段"})

	// restore 之后正文和 chunks 应原样可见。
	if _, err := repo.RestoreDocument(ctx, doc.ID); err != nil {
		t.Fatalf("RestoreDocument: %v", err)
	}
	got, err := repo.GetDocumentContent(ctx, doc.ID)
	if err != nil {
		t.Fatalf("restore 后 GetDocumentContent: %v", err)
	}
	if got.Content != content || got.ContentHash != hash {
		t.Fatalf("restore 后正文/hash 不一致: content=%q hash=%x", got.Content, got.ContentHash)
	}
}

func TestIntegrationGetDocumentContentNotFound(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	ctx := context.Background()

	if _, err := repo.GetDocumentContent(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的文档应返回 ErrNotFound, got %v", err)
	}

	missingID := uuid.New()
	if err := repo.ReplaceDocumentContent(ctx, missingID, "x", sha256.Sum256([]byte("x")), makeChunks("x")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("向不存在的文档写正文应返回 ErrNotFound, got %v", err)
	}

	doc := mustCreateDocument(t, repo, "还没有正文的文档")
	if _, err := repo.GetDocumentContent(ctx, doc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("没有正文的文档应返回 ErrNotFound, got %v", err)
	}
}

func TestIntegrationReplaceDocumentContentRejectsEmptyChunks(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	ctx := context.Background()

	doc := mustCreateDocument(t, repo, "空 chunk 防御测试")
	content := "有正文但没有 chunk"
	if err := repo.ReplaceDocumentContent(ctx, doc.ID, content, sha256.Sum256([]byte(content)), nil); err == nil {
		t.Fatal("空 chunks 应返回错误，避免写出有正文但没有 chunk 的状态")
	}
	if _, err := repo.GetDocumentContent(ctx, doc.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("空 chunks 失败后不应写入正文, got %v", err)
	}
}

func TestIntegrationReplaceDocumentContentRollback(t *testing.T) {
	repo, conn := newIntegrationRepo(t)
	ctx := context.Background()

	doc := mustCreateDocument(t, repo, "正文回滚测试")
	content1 := "旧第一段旧第二段"
	hash1 := sha256.Sum256([]byte(content1))
	chunks1 := makeChunks("旧第一段", "旧第二段")
	if err := repo.ReplaceDocumentContent(ctx, doc.ID, content1, hash1, chunks1); err != nil {
		t.Fatalf("第一次 ReplaceDocumentContent: %v", err)
	}

	// 故意造一个重复的 chunk_index，让第二批 INSERT 撞唯一键。
	// 此时事务里已经删了旧 chunks、改了正文，必须整体回滚到第一次写入的状态。
	badChunks := []ContentChunk{
		{Index: 0, Content: "坏第一段", CharCount: 4},
		{Index: 0, Content: "坏第二段", CharCount: 4},
	}
	if err := repo.ReplaceDocumentContent(ctx, doc.ID, "坏正文", sha256.Sum256([]byte("坏正文")), badChunks); err == nil {
		t.Fatal("重复 chunk_index 应触发唯一键错误")
	}

	got, err := repo.GetDocumentContent(ctx, doc.ID)
	if err != nil {
		t.Fatalf("回滚后 GetDocumentContent: %v", err)
	}
	if got.Content != content1 || got.ContentHash != hash1 {
		t.Fatalf("回滚后正文/hash 被改了: content=%q hash=%x", got.Content, got.ContentHash)
	}
	assertChunkRows(t, conn, doc.ID, []string{"旧第一段", "旧第二段"})
}

func TestIntegrationReplaceDocumentContentBatch(t *testing.T) {
	repo, conn := newIntegrationRepo(t)
	ctx := context.Background()

	doc := mustCreateDocument(t, repo, "正文分批写入测试")

	// 251 个 chunk 会跨过 chunkInsertBatchSize=250 的边界，走两条 INSERT。
	const n = 251
	chunks := make([]ContentChunk, n)
	for i := range chunks {
		text := fmt.Sprintf("chunk-%03d", i)
		chunks[i] = ContentChunk{Index: i, Content: text, CharCount: utf8.RuneCountInString(text)}
	}
	content := "batch content"
	if err := repo.ReplaceDocumentContent(ctx, doc.ID, content, sha256.Sum256([]byte(content)), chunks); err != nil {
		t.Fatalf("ReplaceDocumentContent: %v", err)
	}

	var count int64
	if err := conn.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM document_chunks WHERE document_id = ?`, doc.ID[:],
	).Scan(&count); err != nil {
		t.Fatalf("统计 document_chunks: %v", err)
	}
	if count != n {
		t.Fatalf("chunk 行数 = %d, want %d", count, n)
	}

	var lastIndex int
	var lastContent string
	if err := conn.QueryRowContext(ctx,
		`SELECT chunk_index, content FROM document_chunks WHERE document_id = ? ORDER BY chunk_index DESC LIMIT 1`,
		doc.ID[:],
	).Scan(&lastIndex, &lastContent); err != nil {
		t.Fatalf("读取最后一个 chunk: %v", err)
	}
	if lastIndex != n-1 || lastContent != fmt.Sprintf("chunk-%03d", n-1) {
		t.Fatalf("最后一个 chunk = (%d, %q), want (%d, %q)", lastIndex, lastContent, n-1, fmt.Sprintf("chunk-%03d", n-1))
	}
}

func makeChunks(texts ...string) []ContentChunk {
	chunks := make([]ContentChunk, len(texts))
	for i, text := range texts {
		chunks[i] = ContentChunk{
			Index:     i,
			Content:   text,
			CharCount: utf8.RuneCountInString(text),
		}
	}
	return chunks
}

func mustCreateDocument(t *testing.T, repo *MySQLRepository, title string) *Document {
	t.Helper()

	doc, err := repo.CreateDocument(context.Background(), title, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	return doc
}

func assertChunkRows(t *testing.T, conn *sql.Conn, documentID uuid.UUID, want []string) {
	t.Helper()

	rows, err := conn.QueryContext(context.Background(),
		`SELECT chunk_index, content, char_count FROM document_chunks WHERE document_id = ? ORDER BY chunk_index`,
		documentID[:],
	)
	if err != nil {
		t.Fatalf("查询 document_chunks: %v", err)
	}
	defer func() { _ = rows.Close() }()

	var got []string
	for rows.Next() {
		var index, charCount int
		var content string
		if err := rows.Scan(&index, &content, &charCount); err != nil {
			t.Fatalf("扫描 document_chunks: %v", err)
		}
		if index != len(got) {
			t.Fatalf("chunk_index 不连续: got %d, want %d", index, len(got))
		}
		if charCount != utf8.RuneCountInString(content) {
			t.Fatalf("char_count = %d, want %d (%q)", charCount, utf8.RuneCountInString(content), content)
		}
		got = append(got, content)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("遍历 document_chunks: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("chunk 数 = %d, want %d; got=%v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 个 chunk = %q, want %q", i, got[i], want[i])
		}
	}
}

func assertEmbeddingJob(
	t *testing.T,
	conn *sql.Conn,
	documentID uuid.UUID,
	wantHash [32]byte,
	wantStatus string,
	wantAttempts int,
) {
	t.Helper()
	var (
		hash     []byte
		status   string
		attempts int
	)
	if err := conn.QueryRowContext(context.Background(),
		`SELECT content_hash, status, attempts FROM embedding_jobs WHERE document_id = ?`,
		documentID[:],
	).Scan(&hash, &status, &attempts); err != nil {
		t.Fatalf("查询 embedding_jobs: %v", err)
	}
	if string(hash) != string(wantHash[:]) {
		t.Fatalf("embedding job hash = %x, want %x", hash, wantHash)
	}
	if status != wantStatus || attempts != wantAttempts {
		t.Fatalf("embedding job status/attempts = %s/%d, want %s/%d",
			status, attempts, wantStatus, wantAttempts)
	}
}

func TestIntegrationGetChunksByIDs(t *testing.T) {
	repo, conn := newIntegrationRepo(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)

	doc := mustCreateDocument(t, repo, "chunk 回表测试")
	content := "第一段第二段"
	hash := sha256.Sum256([]byte(content))
	if err := repo.ReplaceDocumentContent(ctx, doc.ID, content, hash, makeChunks("第一段", "第二段")); err != nil {
		t.Fatalf("ReplaceDocumentContent: %v", err)
	}
	stored, err := repo.ListDocumentChunks(ctx, doc.ID)
	if err != nil {
		t.Fatalf("ListDocumentChunks: %v", err)
	}
	ids := []uuid.UUID{stored[0].ID, stored[1].ID}

	views, err := repo.GetChunksByIDs(ctx, ids, nil)
	if err != nil || len(views) != 2 {
		t.Fatalf("GetChunksByIDs = %v, %v", views, err)
	}
	byID := make(map[uuid.UUID]ChunkView, len(views))
	for _, v := range views {
		byID[v.ChunkID] = v
	}
	if byID[ids[0]].Content != "第一段" || byID[ids[1]].DocumentTitle != "chunk 回表测试" {
		t.Fatalf("views = %+v", views)
	}

	collectionID := uuid.New()
	insertTestCollection(t, conn, collectionID, "chunk 回表 collection", nil, now, nil)
	insertTestCollectionDocument(t, conn, collectionID, doc.ID, now)
	views, err = repo.GetChunksByIDs(ctx, ids, &collectionID)
	if err != nil || len(views) != 2 {
		t.Fatalf("collection 过滤后 = %d, %v; want 2", len(views), err)
	}

	otherCollection := uuid.New()
	views, err = repo.GetChunksByIDs(ctx, ids, &otherCollection)
	if err != nil || len(views) != 0 {
		t.Fatalf("不匹配 collection = %d, %v; want 0", len(views), err)
	}

	if err := repo.SoftDeleteDocument(ctx, doc.ID); err != nil {
		t.Fatalf("SoftDeleteDocument: %v", err)
	}
	views, err = repo.GetChunksByIDs(ctx, ids, nil)
	if err != nil || len(views) != 0 {
		t.Fatalf("软删后 = %d, %v; want 0", len(views), err)
	}
}

func assertDocumentEvent(t *testing.T, conn *sql.Conn, documentID uuid.UUID, hash [32]byte) {
	t.Helper()
	var (
		eventType string
		status    string
		payload   []byte
	)
	err := conn.QueryRowContext(context.Background(), `SELECT event_type, status, payload
		FROM document_events WHERE document_id=? ORDER BY created_at DESC LIMIT 1`, documentID[:]).
		Scan(&eventType, &status, &payload)
	if err != nil {
		t.Fatalf("查询 document_events: %v", err)
	}
	if eventType != "document.content.replaced" || status != "pending" {
		t.Fatalf("event type/status = %s/%s", eventType, status)
	}
	if !strings.Contains(string(payload), fmt.Sprintf("%x", hash)) {
		t.Fatalf("event payload = %s, want hash", payload)
	}
}
