// Package workflow 实现 YAML DAG 工作流：拓扑执行、重试、超时、条件、断点续跑。
package workflow

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Definition 是一份 YAML 工作流。
type Definition struct {
	Name  string `yaml:"name" json:"name"`
	Nodes []Node `yaml:"nodes" json:"nodes"`
}

// Node 是一个 DAG 节点。
type Node struct {
	ID        string         `yaml:"id" json:"id"`
	Action    string         `yaml:"action" json:"action"`
	DependsOn []string       `yaml:"depends_on" json:"depends_on"`
	Params    map[string]any `yaml:"params" json:"params"`
	Retries   int            `yaml:"retries" json:"retries"`
	Timeout   string         `yaml:"timeout" json:"timeout"`
	When      string         `yaml:"when" json:"when"`
}

// Parse 解析并校验 YAML。
func Parse(data []byte) (*Definition, error) {
	var def Definition
	if err := yaml.Unmarshal(data, &def); err != nil {
		return nil, fmt.Errorf("workflow: 解析 YAML: %w", err)
	}
	if err := def.Validate(); err != nil {
		return nil, err
	}
	return &def, nil
}

// Validate 校验节点、依赖与 DAG 无环。
func (d *Definition) Validate() error {
	if strings.TrimSpace(d.Name) == "" {
		return errors.New("workflow: name 不能为空")
	}
	if len(d.Nodes) == 0 {
		return errors.New("workflow: nodes 不能为空")
	}
	seen := make(map[string]bool, len(d.Nodes))
	for _, n := range d.Nodes {
		if strings.TrimSpace(n.ID) == "" {
			return errors.New("workflow: node id 不能为空")
		}
		if seen[n.ID] {
			return fmt.Errorf("workflow: node id %q 重复", n.ID)
		}
		seen[n.ID] = true
		if strings.TrimSpace(n.Action) == "" {
			return fmt.Errorf("workflow: node %q action 不能为空", n.ID)
		}
		if n.Retries < 0 {
			return fmt.Errorf("workflow: node %q retries 不能为负数", n.ID)
		}
		if n.Timeout != "" {
			if _, err := time.ParseDuration(n.Timeout); err != nil {
				return fmt.Errorf("workflow: node %q timeout: %w", n.ID, err)
			}
		}
	}
	for _, n := range d.Nodes {
		for _, dep := range n.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("workflow: node %q 依赖不存在的 %q", n.ID, dep)
			}
		}
	}
	if _, err := d.TopoOrder(); err != nil {
		return err
	}
	return nil
}

// TopoOrder 返回稳定拓扑序（同层按 YAML 顺序）。
func (d *Definition) TopoOrder() ([]Node, error) {
	index := make(map[string]int, len(d.Nodes))
	for i, n := range d.Nodes {
		index[n.ID] = i
	}
	indegree := make(map[string]int, len(d.Nodes))
	children := make(map[string][]string, len(d.Nodes))
	for _, n := range d.Nodes {
		indegree[n.ID] = len(n.DependsOn)
		for _, dep := range n.DependsOn {
			children[dep] = append(children[dep], n.ID)
		}
	}
	var order []Node
	done := make(map[string]bool, len(d.Nodes))
	for len(order) < len(d.Nodes) {
		progress := false
		for _, n := range d.Nodes {
			if done[n.ID] || indegree[n.ID] != 0 {
				continue
			}
			order = append(order, n)
			done[n.ID] = true
			progress = true
			for _, child := range children[n.ID] {
				indegree[child]--
			}
		}
		if !progress {
			return nil, errors.New("workflow: 依赖存在环")
		}
	}
	_ = index
	return order, nil
}
