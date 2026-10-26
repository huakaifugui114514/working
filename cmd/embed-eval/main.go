// Command embed-eval 用真实 embedding 服务跑纯向量检索评测。
//
// 它不写 Qdrant、不连 MySQL：只验证 embedding 模型在同一套评测集上的召回。
// 用法：
//
//	go run ./cmd/embed-eval \
//	  -set internal/eval/testdata/search_eval.json \
//	  -ollama http://127.0.0.1:11434
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/huakaifugui114514/working/internal/embedding"
	"github.com/huakaifugui114514/working/internal/eval"
)

func main() {
	setPath := flag.String("set", "internal/eval/testdata/search_eval.json", "评测集 JSON 路径")
	ollamaURL := flag.String("ollama", "http://127.0.0.1:11434", "Ollama base URL")
	model := flag.String("model", "bge-m3", "embedding 模型")
	timeout := flag.Duration("timeout", 120*time.Second, "单次 embedding 请求超时")
	flag.Parse()

	if err := run(*setPath, *ollamaURL, *model, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "embed-eval:", err)
		os.Exit(1)
	}
}

func run(setPath, ollamaURL, model string, timeout time.Duration) error {
	set, err := eval.Load(setPath)
	if err != nil {
		return err
	}
	embedder, err := embedding.NewOllamaEmbedder(embedding.OllamaOptions{
		BaseURL: ollamaURL,
		Model:   model,
		Timeout: timeout,
	})
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout*2)
	defer cancel()

	docTexts := make([]string, len(set.Documents))
	for i, doc := range set.Documents {
		docTexts[i] = doc.Content
	}
	docVectors, err := embedder.Embed(ctx, docTexts)
	if err != nil {
		return fmt.Errorf("embed documents: %w", err)
	}

	queryTexts := make([]string, len(set.Queries))
	for i, q := range set.Queries {
		queryTexts[i] = q.Query
	}
	queryVectors, err := embedder.Embed(ctx, queryTexts)
	if err != nil {
		return fmt.Errorf("embed queries: %w", err)
	}

	found := make([][]string, 0, len(set.Queries))
	for i, q := range set.Queries {
		scores := make([]float64, len(set.Documents))
		for j := range set.Documents {
			scores[j] = cosine(queryVectors[i], docVectors[j])
		}

		ranked := topK(set.Documents, scores, 5)
		found = append(found, ranked)
		fmt.Printf("%-12s query=%-32q top5=%v expected=%v\n", q.Type, q.Query, ranked, q.Expected)
	}

	m := set.Evaluate(found)
	fmt.Printf("VECTOR %s: queries=%d hits=%d recall@5=%.3f mrr=%.3f no_results=%d\n",
		model, m.Queries, m.Hits, m.RecallAt5, m.MRR, m.NoResults)
	return nil
}

func topK(docs []eval.Document, scores []float64, k int) []string {
	type scored struct {
		id    string
		score float64
	}
	all := make([]scored, len(docs))
	for i, doc := range docs {
		all[i] = scored{id: doc.ID, score: scores[i]}
	}
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if all[j].score > all[i].score {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	if k > len(all) {
		k = len(all)
	}
	out := make([]string, 0, k)
	for i := 0; i < k; i++ {
		out = append(out, all[i].id)
	}
	return out
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
