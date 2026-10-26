package workflow

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func testEngine(t *testing.T, def *Definition, actions map[string]Action, store Store) *Engine {
	t.Helper()
	engine, err := NewEngine(def, actions, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}
	return engine
}

func TestEngineSuccessAndTemplate(t *testing.T) {
	def, _ := Parse([]byte(`
name: demo
nodes:
  - id: first
    action: echo
    params: {value: hello}
  - id: second
    action: echo
    depends_on: [first]
    params: {value: "got ${first.value}"}
`))
	actions := map[string]Action{"echo": func(_ context.Context, _ map[string]any, p map[string]any) (map[string]any, error) {
		return map[string]any{"value": p["value"]}, nil
	}}
	store := NewMemoryStore()
	engine := testEngine(t, def, actions, store)
	run, err := engine.Execute(context.Background(), uuid.New(), map[string]any{"document_id": "d1"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if run.Status != StatusSucceeded {
		t.Fatalf("status = %s", run.Status)
	}
	second := run.Output["second"].(map[string]any)
	if second["value"] != "got hello" {
		t.Fatalf("second = %+v", second)
	}
}

func TestEngineRetryAndSkip(t *testing.T) {
	def, _ := Parse([]byte(`
name: demo
nodes:
  - id: flaky
    action: flaky
    retries: 2
  - id: child
    action: noop
    depends_on: [flaky]
`))
	attempts := 0
	actions := map[string]Action{
		"flaky": func(context.Context, map[string]any, map[string]any) (map[string]any, error) {
			attempts++
			if attempts < 3 {
				return nil, errors.New("transient")
			}
			return map[string]any{"ok": true}, nil
		},
		"noop": NoopAction,
	}
	engine := testEngine(t, def, actions, NewMemoryStore())
	run, err := engine.Execute(context.Background(), uuid.New(), nil)
	if err != nil || run.Status != StatusSucceeded || attempts != 3 {
		t.Fatalf("run=%+v err=%v attempts=%d", run, err, attempts)
	}
}

func TestEngineFailureAndResume(t *testing.T) {
	def, _ := Parse([]byte(`
name: demo
nodes:
  - id: a
    action: maybe
  - id: b
    action: noop
    depends_on: [a]
`))
	fail := true
	actions := map[string]Action{
		"maybe": func(context.Context, map[string]any, map[string]any) (map[string]any, error) {
			if fail {
				return nil, errors.New("boom")
			}
			return map[string]any{"ok": true}, nil
		},
		"noop": NoopAction,
	}
	store := NewMemoryStore()
	runID := uuid.New()
	engine := testEngine(t, def, actions, store)
	run, err := engine.Execute(context.Background(), runID, nil)
	if err == nil || run.Status != StatusFailed {
		t.Fatalf("expected failure, run=%+v err=%v", run, err)
	}
	fail = false
	run, err = engine.Execute(context.Background(), runID, nil)
	if err != nil || run.Status != StatusSucceeded {
		t.Fatalf("resume run=%+v err=%v", run, err)
	}
}

func TestEngineWhenCondition(t *testing.T) {
	def, _ := Parse([]byte(`
name: demo
nodes:
  - id: a
    action: noop
    when: "false"
  - id: b
    action: noop
    depends_on: [a]
`))
	engine := testEngine(t, def, map[string]Action{"noop": NoopAction}, NewMemoryStore())
	run, _ := engine.Execute(context.Background(), uuid.New(), nil)
	if run.Status != StatusSucceeded {
		t.Fatalf("status = %s", run.Status)
	}
	nodes, _ := engine.store.ListNodeRuns(context.Background(), run.ID)
	if len(nodes) != 2 {
		t.Fatalf("nodes = %d", len(nodes))
	}
	for _, n := range nodes {
		if n.Status != StatusSkipped {
			t.Fatalf("node %s = %s", n.NodeID, n.Status)
		}
	}
}

func TestNewEngineMissingAction(t *testing.T) {
	def, _ := Parse([]byte("name: x\nnodes: [{id: a, action: missing}]"))
	_, err := NewEngine(def, map[string]Action{}, NewMemoryStore(), nil)
	if err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Fatalf("err = %v", err)
	}
}

var _ = fmt.Sprintf
