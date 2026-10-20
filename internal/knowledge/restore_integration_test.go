//go:build integration

package knowledge

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestIntegrationRestoreCollection(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	ctx := context.Background()

	collection, err := repo.CreateCollection(ctx, "待恢复知识库", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	document, err := repo.CreateDocument(ctx, "关联文档", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	if _, err := repo.AddDocumentToCollection(ctx, collection.ID, document.ID); err != nil {
		t.Fatalf("AddDocumentToCollection: %v", err)
	}
	if err := repo.SoftDeleteCollection(ctx, collection.ID); err != nil {
		t.Fatalf("SoftDeleteCollection: %v", err)
	}
	if _, err := repo.GetCollection(ctx, collection.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("软删后 GetCollection 应返回 ErrNotFound, got %v", err)
	}

	restored, err := repo.RestoreCollection(ctx, collection.ID)
	if err != nil {
		t.Fatalf("RestoreCollection: %v", err)
	}
	if restored.ID != collection.ID || restored.DeletedAt != nil || restored.UpdatedAt.IsZero() {
		t.Fatalf("RestoreCollection = %+v", restored)
	}
	if _, err := repo.GetCollection(ctx, collection.ID); err != nil {
		t.Fatalf("恢复后 GetCollection: %v", err)
	}

	// 软删时保留的关联在恢复后必须仍然存在。
	page, _, err := repo.ListDocumentsByCollection(ctx, collection.ID, nil, 10)
	if err != nil {
		t.Fatalf("恢复后 ListDocumentsByCollection: %v", err)
	}
	if len(page) != 1 || page[0].Document.ID != document.ID {
		t.Fatalf("恢复后关联丢失: %+v", page)
	}

	if _, err := repo.RestoreCollection(ctx, collection.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("恢复 active collection 应返回 ErrConflict, got %v", err)
	}
	if _, err := repo.RestoreCollection(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("恢复不存在的 collection 应返回 ErrNotFound, got %v", err)
	}
}

func TestIntegrationRestoreDocument(t *testing.T) {
	repo, _ := newIntegrationRepo(t)
	ctx := context.Background()

	collection, err := repo.CreateCollection(ctx, "恢复文档测试", "")
	if err != nil {
		t.Fatalf("CreateCollection: %v", err)
	}
	document, err := repo.CreateDocument(ctx, "待恢复文档", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateDocument: %v", err)
	}
	if _, err := repo.AddDocumentToCollection(ctx, collection.ID, document.ID); err != nil {
		t.Fatalf("AddDocumentToCollection: %v", err)
	}
	if err := repo.SoftDeleteDocument(ctx, document.ID); err != nil {
		t.Fatalf("SoftDeleteDocument: %v", err)
	}
	if _, err := repo.GetDocument(ctx, document.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("软删后 GetDocument 应返回 ErrNotFound, got %v", err)
	}

	restored, err := repo.RestoreDocument(ctx, document.ID)
	if err != nil {
		t.Fatalf("RestoreDocument: %v", err)
	}
	if restored.ID != document.ID || restored.DeletedAt != nil || restored.UpdatedAt.IsZero() {
		t.Fatalf("RestoreDocument = %+v", restored)
	}
	if _, err := repo.GetDocument(ctx, document.ID); err != nil {
		t.Fatalf("恢复后 GetDocument: %v", err)
	}

	page, _, err := repo.ListDocumentsByCollection(ctx, collection.ID, nil, 10)
	if err != nil {
		t.Fatalf("恢复后 ListDocumentsByCollection: %v", err)
	}
	if len(page) != 1 || page[0].Document.ID != document.ID {
		t.Fatalf("恢复后关联丢失: %+v", page)
	}

	if _, err := repo.RestoreDocument(ctx, document.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("恢复 active document 应返回 ErrConflict, got %v", err)
	}
	if _, err := repo.RestoreDocument(ctx, uuid.New()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("恢复不存在的 document 应返回 ErrNotFound, got %v", err)
	}
}
