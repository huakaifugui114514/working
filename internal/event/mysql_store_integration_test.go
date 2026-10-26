//go:build integration

package event

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
)

func TestMySQLStoreClaimAndPublish(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, err := database.Open(context.Background(), cfg.Database, slog.Default())
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	ctx := context.Background()
	id, _ := uuid.NewV7()
	docID := uuid.New()
	payload, _ := json.Marshal(map[string]string{"content_hash": "abc"})
	_, err = db.ExecContext(ctx, `
		INSERT INTO document_events
		(id, event_type, document_id, payload, status, attempts, available_at, created_at)
		VALUES (?, 'document.content.replaced', ?, ?, 'pending', 0, UTC_TIMESTAMP(3), UTC_TIMESTAMP(3))`,
		id[:], docID[:], payload)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM document_events WHERE id=?", id[:])
	})

	store := NewMySQLStore(db)
	if _, err := store.RequeueExpired(ctx); err != nil {
		t.Fatalf("RequeueExpired: %v", err)
	}
	events, err := store.Claim(ctx, 10, time.Minute)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	var found *Event
	for i := range events {
		if events[i].ID == id {
			found = &events[i]
			break
		}
	}
	if found == nil || found.DocumentID != docID || found.Attempts != 1 {
		t.Fatalf("未找到本次事件，events = %+v", events)
	}
	if err := store.MarkPublished(ctx, id); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	var status string
	if err := db.QueryRow("SELECT status FROM document_events WHERE id=?", id[:]).Scan(&status); err != nil {
		t.Fatalf("select status: %v", err)
	}
	if status != "published" {
		t.Fatalf("status = %q", status)
	}
}
