// Package eval 定义检索评测集与指标计算。
//
// 它只用于开发/测试，不进入 HTTP 服务路径。
package eval

import (
	"encoding/json"
	"fmt"
	"os"
)

// Document 是评测语料中的一篇文档；当前一个文档对应一个 chunk。
type Document struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Content string `json:"content"`
}

// Query 是一条评测查询。
type Query struct {
	Query    string   `json:"query"`
	Expected []string `json:"expected"`
	Type     string   `json:"type"`
}

// Set 是一套固定评测集。
type Set struct {
	Collection string     `json:"collection"`
	Documents  []Document `json:"documents"`
	Queries    []Query    `json:"queries"`
}

// Load 从 JSON 文件读取评测集。
func Load(path string) (*Set, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取评测集: %w", err)
	}
	var set Set
	if err := json.Unmarshal(raw, &set); err != nil {
		return nil, fmt.Errorf("解析评测集: %w", err)
	}
	if len(set.Documents) == 0 || len(set.Queries) == 0 {
		return nil, fmt.Errorf("评测集必须包含 documents 和 queries")
	}
	return &set, nil
}

// Metrics 是一次评测的汇总结果。
type Metrics struct {
	Queries   int
	Hits      int
	RecallAt5 float64
	MRR       float64
	NoResults int
}

// Evaluate 按 top5 文档计算 Recall@5 和 MRR。
//
// found 是每个 query 的 top5 命中文档 key，顺序即排名。
func (s *Set) Evaluate(found [][]string) Metrics {
	var m Metrics
	m.Queries = len(s.Queries)

	var recallSum, rrSum float64
	for i, q := range s.Queries {
		var ranked []string
		if i < len(found) {
			ranked = found[i]
		}
		if len(ranked) == 0 {
			m.NoResults++
		}

		expected := make(map[string]bool, len(q.Expected))
		for _, id := range q.Expected {
			expected[id] = true
		}

		hit := 0
		firstRank := 0
		for rank, id := range ranked {
			if expected[id] {
				hit++
				if firstRank == 0 {
					firstRank = rank + 1
				}
			}
		}
		if len(q.Expected) > 0 {
			recallSum += float64(hit) / float64(len(q.Expected))
		}
		if firstRank > 0 {
			m.Hits++
			rrSum += 1 / float64(firstRank)
		}
	}

	if m.Queries > 0 {
		m.RecallAt5 = recallSum / float64(m.Queries)
		m.MRR = rrSum / float64(m.Queries)
	}
	return m
}
