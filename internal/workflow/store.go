package workflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ErrNotFound 表示 run 不存在。
var ErrNotFound = errors.New("workflow: run not found")

const (
	StatusPending   = "pending"
	StatusRunning   = "running"
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusSkipped   = "skipped"
)

// Run 是一次工作流运行。
type Run struct {
	ID         uuid.UUID
	Name       string
	Status     string
	Input      map[string]any
	Output     map[string]any
	Error      string
	CreatedAt  time.Time
	StartedAt  time.Time
	FinishedAt time.Time
}

// NodeRun 是单个节点的运行记录。
type NodeRun struct {
	RunID      uuid.UUID
	NodeID     string
	Status     string
	Attempts   int
	Output     map[string]any
	Error      string
	StartedAt  time.Time
	FinishedAt time.Time
}

// Store 是运行记录持久化接口。
type Store interface {
	CreateRun(ctx context.Context, run *Run) error
	UpdateRun(ctx context.Context, run *Run) error
	GetRun(ctx context.Context, id uuid.UUID) (*Run, error)
	UpsertNodeRun(ctx context.Context, nr *NodeRun) error
	ListNodeRuns(ctx context.Context, runID uuid.UUID) ([]NodeRun, error)
}

// MemoryStore 用于单测/本地一次性执行。
type MemoryStore struct {
	mu    sync.Mutex
	runs  map[uuid.UUID]*Run
	nodes map[uuid.UUID]map[string]NodeRun
}

// NewMemoryStore 创建空存储。
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{runs: map[uuid.UUID]*Run{}, nodes: map[uuid.UUID]map[string]NodeRun{}}
}

func (m *MemoryStore) CreateRun(_ context.Context, run *Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.runs[run.ID]; ok {
		return fmt.Errorf("workflow: run %s 已存在", run.ID)
	}
	cp := *run
	m.runs[run.ID] = &cp
	return nil
}

func (m *MemoryStore) UpdateRun(_ context.Context, run *Run) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *run
	m.runs[run.ID] = &cp
	return nil
}

func (m *MemoryStore) GetRun(_ context.Context, id uuid.UUID) (*Run, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	run, ok := m.runs[id]
	if !ok {
		return nil, ErrNotFound
	}
	cp := *run
	return &cp, nil
}

func (m *MemoryStore) UpsertNodeRun(_ context.Context, nr *NodeRun) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.nodes[nr.RunID] == nil {
		m.nodes[nr.RunID] = map[string]NodeRun{}
	}
	m.nodes[nr.RunID][nr.NodeID] = *nr
	return nil
}

func (m *MemoryStore) ListNodeRuns(_ context.Context, runID uuid.UUID) ([]NodeRun, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]NodeRun, 0, len(m.nodes[runID]))
	for _, nr := range m.nodes[runID] {
		out = append(out, nr)
	}
	return out, nil
}

// MySQLStore 把运行记录落到 MySQL。
type MySQLStore struct{ db *sql.DB }

// NewMySQLStore 注入连接池。
func NewMySQLStore(db *sql.DB) *MySQLStore {
	if db == nil {
		panic("workflow.NewMySQLStore: db is nil")
	}
	return &MySQLStore{db: db}
}

func (s *MySQLStore) CreateRun(ctx context.Context, run *Run) error {
	input, err := json.Marshal(run.Input)
	if err != nil {
		return fmt.Errorf("workflow: marshal input: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_runs
		(id, workflow_name, status, input, created_at)
		VALUES (?, ?, ?, ?, ?)`, run.ID[:], run.Name, run.Status, input, run.CreatedAt.UTC())
	if err != nil {
		return fmt.Errorf("workflow: create run: %w", err)
	}
	return nil
}

func (s *MySQLStore) UpdateRun(ctx context.Context, run *Run) error {
	output, err := json.Marshal(run.Output)
	if err != nil {
		return fmt.Errorf("workflow: marshal output: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `UPDATE workflow_runs
		SET status=?, output=?, last_error=?, started_at=?, finished_at=?
		WHERE id=?`, run.Status, output, nullString(run.Error), nullTime(run.StartedAt), nullTime(run.FinishedAt), run.ID[:])
	if err != nil {
		return fmt.Errorf("workflow: update run: %w", err)
	}
	return nil
}

func (s *MySQLStore) GetRun(ctx context.Context, id uuid.UUID) (*Run, error) {
	var (
		run      Run
		idRaw    []byte
		input    []byte
		output   []byte
		errText  sql.NullString
		started  sql.NullTime
		finished sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, `SELECT id, workflow_name, status, input, output, last_error,
		created_at, started_at, finished_at FROM workflow_runs WHERE id=?`, id[:]).
		Scan(&idRaw, &run.Name, &run.Status, &input, &output, &errText, &run.CreatedAt, &started, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("workflow: get run: %w", err)
	}
	run.ID = id
	if len(input) > 0 {
		_ = json.Unmarshal(input, &run.Input)
	}
	if len(output) > 0 {
		_ = json.Unmarshal(output, &run.Output)
	}
	if errText.Valid {
		run.Error = errText.String
	}
	if started.Valid {
		run.StartedAt = started.Time
	}
	if finished.Valid {
		run.FinishedAt = finished.Time
	}
	return &run, nil
}

func (s *MySQLStore) UpsertNodeRun(ctx context.Context, nr *NodeRun) error {
	output, err := json.Marshal(nr.Output)
	if err != nil {
		return fmt.Errorf("workflow: marshal node output: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO workflow_node_runs
		(run_id, node_id, status, attempts, output, last_error, started_at, finished_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) AS new
		ON DUPLICATE KEY UPDATE
		status=new.status, attempts=new.attempts, output=new.output,
		last_error=new.last_error, started_at=new.started_at, finished_at=new.finished_at`,
		nr.RunID[:], nr.NodeID, nr.Status, nr.Attempts, output, nullString(nr.Error), nullTime(nr.StartedAt), nullTime(nr.FinishedAt))
	if err != nil {
		return fmt.Errorf("workflow: upsert node run: %w", err)
	}
	return nil
}

func (s *MySQLStore) ListNodeRuns(ctx context.Context, runID uuid.UUID) ([]NodeRun, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT run_id, node_id, status, attempts, output, last_error,
		started_at, finished_at FROM workflow_node_runs WHERE run_id=?`, runID[:])
	if err != nil {
		return nil, fmt.Errorf("workflow: list node runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []NodeRun
	for rows.Next() {
		var (
			nr       NodeRun
			runRaw   []byte
			output   []byte
			errText  sql.NullString
			started  sql.NullTime
			finished sql.NullTime
		)
		if err := rows.Scan(&runRaw, &nr.NodeID, &nr.Status, &nr.Attempts, &output, &errText, &started, &finished); err != nil {
			return nil, fmt.Errorf("workflow: scan node run: %w", err)
		}
		nr.RunID = runID
		if len(output) > 0 {
			_ = json.Unmarshal(output, &nr.Output)
		}
		if errText.Valid {
			nr.Error = errText.String
		}
		if started.Valid {
			nr.StartedAt = started.Time
		}
		if finished.Valid {
			nr.FinishedAt = finished.Time
		}
		out = append(out, nr)
	}
	return out, rows.Err()
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}
