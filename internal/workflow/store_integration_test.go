//go:build integration

package workflow

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/config"
	"github.com/huakaifugui114514/working/internal/database"
)

func TestMySQLStoreRunAndNode(t *testing.T) {
	cfg, err := config.Load("")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, err := database.Open(context.Background(), cfg.Database, slog.Default())
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	defer func() { _ = db.Close() }()

	store := NewMySQLStore(db)
	ctx := context.Background()
	runID := uuid.New()
	run := &Run{ID: runID, Name: "test", Status: StatusRunning, Input: map[string]any{"x": "y"}, CreatedAt: time.Now().UTC()}
	if err := store.CreateRun(ctx, run); err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM workflow_node_runs WHERE run_id=?", runID[:])
		_, _ = db.Exec("DELETE FROM workflow_runs WHERE id=?", runID[:])
	})
	node := &NodeRun{RunID: runID, NodeID: "a", Status: StatusSucceeded, Attempts: 1,
		Output: map[string]any{"ok": true}, StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}
	if err := store.UpsertNodeRun(ctx, node); err != nil {
		t.Fatalf("UpsertNodeRun: %v", err)
	}
	run.Status = StatusSucceeded
	run.Output = map[string]any{"a": map[string]any{"ok": true}}
	run.FinishedAt = time.Now().UTC()
	if err := store.UpdateRun(ctx, run); err != nil {
		t.Fatalf("UpdateRun: %v", err)
	}
	got, err := store.GetRun(ctx, runID)
	if err != nil || got.Status != StatusSucceeded || got.Input["x"] != "y" {
		t.Fatalf("GetRun = %+v, %v", got, err)
	}
	nodes, err := store.ListNodeRuns(ctx, runID)
	if err != nil || len(nodes) != 1 || nodes[0].NodeID != "a" {
		t.Fatalf("ListNodeRuns = %+v, %v", nodes, err)
	}
}
