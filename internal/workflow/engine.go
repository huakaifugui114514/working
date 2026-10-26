package workflow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Action 是一个可执行节点。vars 包含 input 和已完成节点的输出；
// params 已经过 ${...} 模板展开。
type Action func(ctx context.Context, vars map[string]any, params map[string]any) (map[string]any, error)

// RunError 汇总节点失败，保留 Unwrap 便于调用方 errors.Is。
type RunError struct{ Failures []error }

func (e *RunError) Error() string {
	parts := make([]string, 0, len(e.Failures))
	for _, err := range e.Failures {
		parts = append(parts, err.Error())
	}
	return "workflow: run 失败: " + strings.Join(parts, "; ")
}

func (e *RunError) Unwrap() []error { return e.Failures }

// Engine 执行一份 Definition。
type Engine struct {
	def     *Definition
	actions map[string]Action
	store   Store
	logger  *slog.Logger
}

// NewEngine 校验 action 是否齐全；store 不能为空。
func NewEngine(def *Definition, actions map[string]Action, store Store, logger *slog.Logger) (*Engine, error) {
	if def == nil || store == nil {
		return nil, fmt.Errorf("workflow: definition/store 不能为空")
	}
	if err := def.Validate(); err != nil {
		return nil, err
	}
	for _, n := range def.Nodes {
		if actions[n.Action] == nil {
			return nil, fmt.Errorf("workflow: node %q 的 action %q 未注册", n.ID, n.Action)
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{def: def, actions: actions, store: store, logger: logger}, nil
}

// Execute 执行/续跑一次 run。runID 已存在时跳过 succeeded 节点。
func (e *Engine) Execute(ctx context.Context, runID uuid.UUID, input map[string]any) (*Run, error) {
	now := time.Now().UTC()
	run, err := e.store.GetRun(ctx, runID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if errors.Is(err, ErrNotFound) {
		run = &Run{
			ID: runID, Name: e.def.Name, Status: StatusPending,
			Input: input, CreatedAt: now,
		}
		if err := e.store.CreateRun(ctx, run); err != nil {
			return nil, err
		}
	}
	if run.Status == StatusSucceeded {
		return run, nil
	}
	run.Status = StatusRunning
	run.StartedAt = now
	run.Error = ""
	run.Output = map[string]any{}
	if err := e.store.UpdateRun(ctx, run); err != nil {
		return nil, err
	}

	nodeRuns, err := e.store.ListNodeRuns(ctx, runID)
	if err != nil {
		return nil, err
	}
	existing := make(map[string]NodeRun, len(nodeRuns))
	for _, nr := range nodeRuns {
		existing[nr.NodeID] = nr
	}

	vars := map[string]any{"input": input, "run_id": runID.String()}
	outputs := map[string]any{}
	status := map[string]string{}
	var failures []error

	order, err := e.def.TopoOrder()
	if err != nil {
		return nil, err
	}
	for _, node := range order {
		if nr, ok := existing[node.ID]; ok && nr.Status == StatusSucceeded {
			status[node.ID] = StatusSucceeded
			outputs[node.ID] = nr.Output
			vars[node.ID] = nr.Output
			continue
		}
		blocked := false
		for _, dep := range node.DependsOn {
			if status[dep] != StatusSucceeded {
				blocked = true
				break
			}
		}
		if blocked {
			status[node.ID] = StatusSkipped
			e.saveNode(ctx, runID, node.ID, StatusSkipped, 0, nil, "依赖未成功", now)
			continue
		}
		if !e.when(ctx, node.When, vars) {
			status[node.ID] = StatusSkipped
			e.saveNode(ctx, runID, node.ID, StatusSkipped, 0, nil, "when 条件不满足", now)
			continue
		}

		params, err := renderMap(node.Params, vars)
		if err != nil {
			return nil, fmt.Errorf("workflow: node %q params: %w", node.ID, err)
		}
		action := e.actions[node.Action]
		timeout := 30 * time.Second
		if node.Timeout != "" {
			timeout, _ = time.ParseDuration(node.Timeout)
		}
		maxAttempts := node.Retries + 1
		var lastErr error
		for attempt := 1; attempt <= maxAttempts; attempt++ {
			nr := NodeRun{RunID: runID, NodeID: node.ID, Status: StatusRunning, Attempts: attempt, StartedAt: time.Now().UTC()}
			if err := e.store.UpsertNodeRun(ctx, &nr); err != nil {
				return nil, err
			}
			runCtx, cancel := context.WithTimeout(ctx, timeout)
			out, actionErr := action(runCtx, vars, params)
			cancel()
			nr.FinishedAt = time.Now().UTC()
			if actionErr == nil {
				nr.Status = StatusSucceeded
				nr.Output = out
				if err := e.store.UpsertNodeRun(ctx, &nr); err != nil {
					return nil, err
				}
				status[node.ID] = StatusSucceeded
				outputs[node.ID] = out
				vars[node.ID] = out
				lastErr = nil
				break
			}
			lastErr = actionErr
			nr.Status = StatusFailed
			nr.Error = actionErr.Error()
			if err := e.store.UpsertNodeRun(ctx, &nr); err != nil {
				return nil, err
			}
			if attempt < maxAttempts {
				e.logger.Warn("workflow 节点重试", "node", node.ID, "attempt", attempt, "error", actionErr)
			}
		}
		if lastErr != nil {
			status[node.ID] = StatusFailed
			failures = append(failures, fmt.Errorf("%s: %w", node.ID, lastErr))
		}
	}

	run.FinishedAt = time.Now().UTC()
	run.Output = outputs
	var runErr error
	if len(failures) > 0 {
		run.Status = StatusFailed
		runErr = &RunError{Failures: failures}
		run.Error = runErr.Error()
	} else {
		run.Status = StatusSucceeded
	}
	if err := e.store.UpdateRun(ctx, run); err != nil {
		return nil, err
	}
	if run.Status == StatusFailed {
		return run, runErr
	}
	return run, nil
}

func (e *Engine) saveNode(ctx context.Context, runID uuid.UUID, nodeID, status string, attempts int, output map[string]any, errMsg string, t time.Time) {
	nr := NodeRun{RunID: runID, NodeID: nodeID, Status: status, Attempts: attempts, Output: output, Error: errMsg, StartedAt: t, FinishedAt: t}
	if err := e.store.UpsertNodeRun(ctx, &nr); err != nil {
		e.logger.Error("workflow 保存节点失败", "node", nodeID, "error", err)
	}
}

func (e *Engine) when(ctx context.Context, expr string, vars map[string]any) bool {
	_ = ctx
	if strings.TrimSpace(expr) == "" {
		return true
	}
	value, err := renderString(expr, vars)
	if err != nil {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		return v != "" && v != "false" && v != "0"
	default:
		return v != nil
	}
}

var templateRE = regexp.MustCompile(`\$\{([^}]+)\}`)

func renderMap(in map[string]any, vars map[string]any) (map[string]any, error) {
	if in == nil {
		return map[string]any{}, nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		rendered, err := renderValue(v, vars)
		if err != nil {
			return nil, err
		}
		out[k] = rendered
	}
	return out, nil
}

func renderValue(v any, vars map[string]any) (any, error) {
	switch x := v.(type) {
	case string:
		return renderString(x, vars)
	case map[string]any:
		return renderMap(x, vars)
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			rendered, err := renderValue(item, vars)
			if err != nil {
				return nil, err
			}
			out[i] = rendered
		}
		return out, nil
	default:
		return v, nil
	}
}

func renderString(s string, vars map[string]any) (any, error) {
	matches := templateRE.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return s, nil
	}
	if len(matches) == 1 && matches[0][0] == 0 && matches[0][1] == len(s) {
		return resolvePath(vars, s[2:len(s)-1])
	}
	var b strings.Builder
	last := 0
	for _, m := range matches {
		b.WriteString(s[last:m[0]])
		value, err := resolvePath(vars, s[m[2]:m[3]])
		if err != nil {
			return nil, err
		}
		fmt.Fprint(&b, value)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String(), nil
}

func resolvePath(vars map[string]any, path string) (any, error) {
	parts := strings.Split(strings.TrimSpace(path), ".")
	var current any = vars
	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("workflow: 无法解析变量 %q", path)
		}
		current, ok = m[part]
		if !ok {
			return nil, fmt.Errorf("workflow: 变量 %q 不存在", path)
		}
	}
	return current, nil
}
