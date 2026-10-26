// Package embedding 定义文本向量化接口与本地 Ollama 实现。
//
// MySQL 里的 chunk 是事实源；向量是可重建的派生数据。
package embedding

import (
	"context"
	"errors"
)

// ErrEmptyInput 表示调用方传入了空文本列表。
var ErrEmptyInput = errors.New("embedding: 输入不能为空")

// Embedder 把一批文本转成向量。
//
// 契约：
//   - 返回的向量数与 texts 数量一致；
//   - 每个向量非空，维度由模型决定（bge-m3 为 1024）；
//   - 单次调用失败时返回 error，不返回部分结果；
//   - 实现必须接收 ctx。
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}
