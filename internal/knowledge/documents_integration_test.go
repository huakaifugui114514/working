//go:build integration

package knowledge

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
)

// TestIntegrationDocumentCRUD 覆盖 documents 表本身的 CRUD、PATCH 三态与软删。
func TestIntegrationDocumentCRUD(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	ctx := context.Background()

	sourceURI := "s3://knowflow/docs/design.pdf"
	mimeType := "application/pdf"
	sizeBytes := int64(2048)
	created, err := repo.CreateDocument(ctx, "设计文档", &sourceURI, &mimeType, &sizeBytes)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	if created.ID == uuid.Nil {
		t.Fatal("CreateDocument 应生成非零 UUID")
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatal("CreateDocument 应填 CreatedAt / UpdatedAt")
	}
	if created.DeletedAt != nil {
		t.Fatal("新建文档的 DeletedAt 应为 nil")
	}

	got, err := repo.GetDocument(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.Title != "设计文档" || got.SourceURI == nil || *got.SourceURI != sourceURI ||
		got.MimeType == nil || *got.MimeType != mimeType ||
		got.SizeBytes == nil || *got.SizeBytes != sizeBytes {
		t.Fatalf("GetDocument = %+v, want source=%q mime=%q size=%d", got, sourceURI, mimeType, sizeBytes)
	}
	if !got.CreatedAt.Equal(created.CreatedAt) || !got.UpdatedAt.Equal(created.UpdatedAt) {
		t.Fatalf("时间往返不一致: got=%v/%v want=%v/%v",
			got.CreatedAt, got.UpdatedAt, created.CreatedAt, created.UpdatedAt)
	}

	if _, err := repo.GetDocument(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在的 id 应返回 ErrNotFound, got %v", err)
	}

	time.Sleep(5 * time.Millisecond)
	newTitle := "新标题"
	updated, err := repo.UpdateDocument(ctx, created.ID, DocumentUpdate{Title: &newTitle})
	if err != nil {
		t.Fatalf("UpdateDocument(title): %v", err)
	}
	if updated.Title != newTitle || updated.SourceURI == nil || *updated.SourceURI != sourceURI ||
		updated.SizeBytes == nil || *updated.SizeBytes != sizeBytes {
		t.Fatalf("只改 title 却动了其他字段: %+v", updated)
	}
	if !updated.UpdatedAt.After(created.UpdatedAt) {
		t.Fatalf("UpdatedAt 未刷新: old=%v new=%v", created.UpdatedAt, updated.UpdatedAt)
	}

	clearedSource, err := repo.UpdateDocument(ctx, created.ID, DocumentUpdate{
		SourceURI: Optional[string]{Set: true},
	})
	if err != nil {
		t.Fatalf("UpdateDocument(clear source_uri): %v", err)
	}
	if clearedSource.SourceURI != nil {
		t.Fatalf("source_uri 应被清成 NULL, got %v", *clearedSource.SourceURI)
	}
	if clearedSource.MimeType == nil || *clearedSource.MimeType != mimeType {
		t.Fatalf("清空 source_uri 不应影响 mime_type: %+v", clearedSource)
	}

	clearedSize, err := repo.UpdateDocument(ctx, created.ID, DocumentUpdate{
		SizeBytes: Optional[int64]{Set: true},
	})
	if err != nil {
		t.Fatalf("UpdateDocument(clear size_bytes): %v", err)
	}
	if clearedSize.SizeBytes != nil {
		t.Fatalf("size_bytes 应被清成 NULL, got %v", *clearedSize.SizeBytes)
	}

	same, err := repo.UpdateDocument(ctx, created.ID, DocumentUpdate{})
	if err != nil {
		t.Fatalf("空 PATCH 应为 no-op: %v", err)
	}
	if same.Title != newTitle || same.SourceURI != nil || same.SizeBytes != nil ||
		!same.UpdatedAt.Equal(clearedSize.UpdatedAt) {
		t.Fatalf("空 PATCH 不应改数据: %+v", same)
	}

	plain, err := repo.CreateDocument(ctx, "无元数据", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateDocument(nil 可空字段): %v", err)
	}
	gotPlain, err := repo.GetDocument(ctx, plain.ID)
	if err != nil {
		t.Fatalf("GetDocument(nil 可空字段): %v", err)
	}
	if gotPlain.SourceURI != nil || gotPlain.MimeType != nil || gotPlain.SizeBytes != nil {
		t.Fatalf("NULL 字段应保持 nil: %+v", gotPlain)
	}

	if err := repo.SoftDeleteDocument(ctx, created.ID); err != nil {
		t.Fatalf("SoftDeleteDocument: %v", err)
	}
	if _, err := repo.GetDocument(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("软删后 GetDocument 应返回 ErrNotFound, got %v", err)
	}
	if _, err := repo.UpdateDocument(ctx, created.ID, DocumentUpdate{Title: &newTitle}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("软删后 UpdateDocument 应返回 ErrNotFound, got %v", err)
	}
	if err := repo.SoftDeleteDocument(ctx, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复软删应返回 ErrNotFound, got %v", err)
	}
}

// TestIntegrationDocumentAssociation 覆盖 collection_documents 的添加、重复冲突、
// 软删资源、移除与"移除关联不删文档"。
func TestIntegrationDocumentAssociation(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	ctx := context.Background()

	collection, err := repo.CreateCollection(ctx, "关联测试", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	document, err := repo.CreateDocument(ctx, "关联文档", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}

	cd, err := repo.AddDocumentToCollection(ctx, collection.ID, document.ID)
	if err != nil {
		t.Fatalf("AddDocumentToCollection: %v", err)
	}
	if cd.Document == nil || cd.Document.ID != document.ID {
		t.Fatalf("返回的 Document = %+v, want id=%v", cd.Document, document.ID)
	}
	if cd.AddedAt.IsZero() {
		t.Fatal("AddDocumentToCollection 应填 AddedAt")
	}

	if _, err := repo.AddDocumentToCollection(ctx, collection.ID, document.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("重复添加应返回 ErrConflict, got %v", err)
	}
	if _, err := repo.AddDocumentToCollection(ctx, uuid.New(), document.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("collection 不存在应返回 ErrNotFound, got %v", err)
	}
	if _, err := repo.AddDocumentToCollection(ctx, collection.ID, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("document 不存在应返回 ErrNotFound, got %v", err)
	}

	deletedDocument, err := repo.CreateDocument(ctx, "已软删文档", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	if err := repo.SoftDeleteDocument(ctx, deletedDocument.ID); err != nil {
		t.Fatalf("SoftDeleteDocument: %v", err)
	}
	if _, err := repo.AddDocumentToCollection(ctx, collection.ID, deletedDocument.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已软删 document 应返回 ErrNotFound, got %v", err)
	}

	deletedCollection, err := repo.CreateCollection(ctx, "已软删知识库", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if err := repo.SoftDeleteCollection(ctx, deletedCollection.ID); err != nil {
		t.Fatalf("SoftDeleteCollection: %v", err)
	}
	if _, err := repo.AddDocumentToCollection(ctx, deletedCollection.ID, document.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已软删 collection 应返回 ErrNotFound, got %v", err)
	}

	if err := repo.RemoveDocumentFromCollection(ctx, collection.ID, document.ID); err != nil {
		t.Fatalf("RemoveDocumentFromCollection: %v", err)
	}
	if err := repo.RemoveDocumentFromCollection(ctx, collection.ID, document.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("重复移除应返回 ErrNotFound, got %v", err)
	}
	if _, err := repo.GetDocument(ctx, document.ID); err != nil {
		t.Fatalf("移除关联不应删除文档: %v", err)
	}

	if _, err := repo.AddDocumentToCollection(ctx, collection.ID, document.ID); err != nil {
		t.Fatalf("移除后重新添加: %v", err)
	}
	secondCollection, err := repo.CreateCollection(ctx, "第二个知识库", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if _, err := repo.AddDocumentToCollection(ctx, secondCollection.ID, document.ID); err != nil {
		t.Fatalf("同一文档应可加入多个知识库: %v", err)
	}
}

// TestIntegrationListDocumentsByCollection 覆盖关联列表的 keyset 分页：
// 同一毫秒按 document_id 排序、跨毫秒游标、软删过滤与 collection 不存在。
func TestIntegrationListDocumentsByCollection(t *testing.T) {
	repo, conn := newIntegrationRepo(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Millisecond)
	collectionID := testUUID(100)
	insertTestCollection(t, conn, collectionID, "列表测试", nil, base.Add(-time.Hour), nil)

	ids := make([]uuid.UUID, 6)
	for i := range ids {
		ids[i] = testUUID(i + 1)
		insertTestDocument(t, conn, ids[i], fmt.Sprintf("doc-%d", i+1), nil, nil, nil,
			base.Add(-time.Duration(i+1)*time.Millisecond), nil)
	}

	// doc1 最早加入，doc2 次之，doc3/doc4/doc5 在同一毫秒；
	// doc6 没有关联；doc7 已软删，即使关联时间最新也不能出现。
	insertTestCollectionDocument(t, conn, collectionID, ids[0], base.Add(-2*time.Millisecond))
	insertTestCollectionDocument(t, conn, collectionID, ids[1], base.Add(-time.Millisecond))
	insertTestCollectionDocument(t, conn, collectionID, ids[2], base)
	insertTestCollectionDocument(t, conn, collectionID, ids[3], base)
	insertTestCollectionDocument(t, conn, collectionID, ids[4], base)

	deletedID := testUUID(7)
	deletedAt := base
	insertTestDocument(t, conn, deletedID, "deleted-doc", nil, nil, nil, base, &deletedAt)
	insertTestCollectionDocument(t, conn, collectionID, deletedID, base.Add(time.Millisecond))

	page, next, err := repo.ListDocumentsByCollection(ctx, collectionID, nil, 2)
	if err != nil {
		t.Fatalf("第一页: %v", err)
	}
	if len(page) != 2 || page[0].Document.ID != ids[4] || page[1].Document.ID != ids[3] {
		t.Fatalf("第一页 = %v, want [%v %v]", documentIDs(page), ids[4], ids[3])
	}
	if next == nil || next.ID != ids[3] || !next.CreatedAt.Equal(base) {
		t.Fatalf("第一页 next = %+v, want (base, %v)", next, ids[3])
	}

	page, next, err = repo.ListDocumentsByCollection(ctx, collectionID, next, 2)
	if err != nil {
		t.Fatalf("第二页: %v", err)
	}
	if len(page) != 2 || page[0].Document.ID != ids[2] || page[1].Document.ID != ids[1] {
		t.Fatalf("第二页 = %v, want [%v %v]", documentIDs(page), ids[2], ids[1])
	}
	if next == nil || next.ID != ids[1] {
		t.Fatalf("第二页 next = %+v, want id=%v", next, ids[1])
	}

	page, next, err = repo.ListDocumentsByCollection(ctx, collectionID, next, 2)
	if err != nil {
		t.Fatalf("第三页: %v", err)
	}
	if len(page) != 1 || page[0].Document.ID != ids[0] {
		t.Fatalf("第三页 = %v, want [%v]", documentIDs(page), ids[0])
	}
	if next != nil {
		t.Fatalf("最后一页 next 应为 nil, got %+v", next)
	}

	all, next, err := repo.ListDocumentsByCollection(ctx, collectionID, nil, 5)
	if err != nil {
		t.Fatalf("刚好一页: %v", err)
	}
	if len(all) != 5 || next != nil {
		t.Fatalf("刚好一页: len=%d next=%+v, want len=5 next=nil", len(all), next)
	}
	for _, item := range all {
		if item.Document.ID == deletedID {
			t.Fatal("已软删的文档不应出现在列表里")
		}
	}

	old := &Cursor{CreatedAt: base.Add(-time.Hour), ID: ids[0]}
	empty, next, err := repo.ListDocumentsByCollection(ctx, collectionID, old, 2)
	if err != nil {
		t.Fatalf("空页: %v", err)
	}
	if len(empty) != 0 || next != nil {
		t.Fatalf("空页 = %v, next=%+v, want empty + nil", documentIDs(empty), next)
	}

	cur := &Cursor{CreatedAt: base, ID: ids[3]}
	before := *cur
	if _, _, err := repo.ListDocumentsByCollection(ctx, collectionID, cur, 1); err != nil {
		t.Fatalf("ListDocumentsByCollection: %v", err)
	}
	if *cur != before {
		t.Fatalf("ListDocumentsByCollection 修改了调用方的 cursor: got %+v, want %+v", *cur, before)
	}

	if _, _, err := repo.ListDocumentsByCollection(ctx, uuid.New(), nil, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("collection 不存在应返回 ErrNotFound, got %v", err)
	}

	deletedCollection, err := repo.CreateCollection(ctx, "列表软删", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	if err := repo.SoftDeleteCollection(ctx, deletedCollection.ID); err != nil {
		t.Fatalf("SoftDeleteCollection: %v", err)
	}
	if _, _, err := repo.ListDocumentsByCollection(ctx, deletedCollection.ID, nil, 2); !errors.Is(err, ErrNotFound) {
		t.Fatalf("已软删 collection 应返回 ErrNotFound, got %v", err)
	}

	emptyCollection, err := repo.CreateCollection(ctx, "空列表", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	emptyItems, next, err := repo.ListDocumentsByCollection(ctx, emptyCollection.ID, nil, 2)
	if err != nil {
		t.Fatalf("空 collection: %v", err)
	}
	if emptyItems == nil || len(emptyItems) != 0 || next != nil {
		t.Fatalf("空 collection = %v, next=%+v, want 非 nil 空切片 + nil", documentIDs(emptyItems), next)
	}
}

func documentIDs(items []*CollectionDocument) []uuid.UUID {
	ids := make([]uuid.UUID, len(items))
	for i, item := range items {
		ids[i] = item.Document.ID
	}
	return ids
}

// TestIntegrationDocumentServiceNormalizesEmptyStrings 验证 service 层把空串/纯空白
// 折叠成 SQL NULL，而不是让空串落库（列表/查询时 IS NULL 才不会漏行）。
func TestIntegrationDocumentServiceNormalizesEmptyStrings(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	svc := NewDocumentService(repo)
	ctx := context.Background()

	blank := "   "
	created, err := svc.CreateDocument(ctx, "空串字段", &blank, &blank, nil)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	got, err := repo.GetDocument(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetDocument: %v", err)
	}
	if got.SourceURI != nil || got.MimeType != nil {
		t.Fatalf("空串应折叠为 NULL: %+v", got)
	}

	empty := ""
	updated, err := svc.UpdateDocument(ctx, created.ID, DocumentUpdate{
		SourceURI: Optional[string]{Set: true, Value: &empty},
		MimeType:  Optional[string]{Set: true, Value: &blank},
	})
	if err != nil {
		t.Fatalf("UpdateDocument: %v", err)
	}
	if updated.SourceURI != nil || updated.MimeType != nil {
		t.Fatalf("PATCH 空串应清成 NULL: %+v", updated)
	}
}
