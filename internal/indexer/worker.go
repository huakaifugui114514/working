package indexer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/embedding"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/qdrant"
)

const (
	defaultQdrantBatch = 128
	maxBackoff         = 5 * time.Minute
)

// VectorStore 是 worker 依赖的向量写入接口。
//
// Workers 只需要删除和 upsert；查询留给后面的 HybridRetriever。
type VectorStore interface {
	DeleteByDocument(ctx context.Context, documentID uuid.UUID) error
	Upsert(ctx context.Context, points []qdrant.Point) error
}

// WorkerOptions 是 embedding worker 的依赖与参数。
type WorkerOptions struct {
	Jobs        JobQueue
	Content     knowledge.ContentRepository
	Embedder    embedding.Embedder
	Vectors     VectorStore
	WorkerID    string
	Model       string
	BatchSize   int
	Lease       time.Duration
	MaxAttempts int
	Logger      *slog.Logger
}

// Worker 负责从 embedding_jobs 取任务并写 Qdrant。
type Worker struct {
	jobs        JobQueue
	content     knowledge.ContentRepository
	embedder    embedding.Embedder
	vectors     VectorStore
	workerID    string
	model       string
	batchSize   int
	lease       time.Duration
	maxAttempts int
	logger      *slog.Logger
}

// NewWorker 校验依赖并构造 worker。
func NewWorker(opts WorkerOptions) (*Worker, error) {
	if opts.Jobs == nil {
		return nil, fmt.Errorf("indexer: jobs is nil")
	}
	if opts.Content == nil {
		return nil, fmt.Errorf("indexer: content repository is nil")
	}
	if opts.Embedder == nil {
		return nil, fmt.Errorf("indexer: embedder is nil")
	}
	if opts.Vectors == nil {
		return nil, fmt.Errorf("indexer: qdrant client is nil")
	}
	if opts.WorkerID == "" {
		return nil, fmt.Errorf("indexer: workerID 不能为空")
	}
	if opts.Model == "" {
		return nil, fmt.Errorf("indexer: model 不能为空")
	}
	if opts.BatchSize < 1 || opts.BatchSize > 128 {
		return nil, fmt.Errorf("indexer: batchSize 必须在 1~128 之间")
	}
	if opts.Lease <= 0 {
		return nil, fmt.Errorf("indexer: lease 必须为正数")
	}
	if opts.MaxAttempts < 1 {
		return nil, fmt.Errorf("indexer: maxAttempts 必须为正数")
	}
	if opts.Logger == nil {
		return nil, fmt.Errorf("indexer: logger is nil")
	}
	return &Worker{
		jobs:        opts.Jobs,
		content:     opts.Content,
		embedder:    opts.Embedder,
		vectors:     opts.Vectors,
		workerID:    opts.WorkerID,
		model:       opts.Model,
		batchSize:   opts.BatchSize,
		lease:       opts.Lease,
		maxAttempts: opts.MaxAttempts,
		logger:      opts.Logger,
	}, nil
}

// RunOnce 处理一条任务。返回 false 表示当前没有可执行任务。
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	requeued, err := w.jobs.RequeueExpired(ctx, w.lease, now)
	if err != nil {
		return false, err
	}
	if requeued > 0 {
		w.logger.Warn("回收过期 embedding 任务", "count", requeued)
	}

	job, err := w.jobs.Claim(ctx, w.workerID, now)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}

	w.logger.Info("开始 embedding 任务",
		"document_id", job.DocumentID,
		"chunker_version", job.ChunkerVersion,
		"attempts", job.Attempts,
	)

	if err := w.process(ctx, *job); err != nil {
		if errors.Is(err, ErrJobStale) {
			w.logger.Warn("embedding 任务已过期，跳过", "document_id", job.DocumentID)
			return true, nil
		}
		if failErr := w.fail(ctx, *job, err); failErr != nil {
			w.logger.Error("记录 embedding 失败状态失败",
				"document_id", job.DocumentID, "error", failErr)
		}
		return true, fmt.Errorf("process document %s: %w", job.DocumentID, err)
	}
	return true, nil
}

func (w *Worker) process(ctx context.Context, job Job) error {
	content, err := w.content.GetDocumentContent(ctx, job.DocumentID)
	if errors.Is(err, knowledge.ErrNotFound) {
		// 文档软删/不存在：不删已有向量（restore 后还能用），只把任务结束。
		return w.complete(ctx, job)
	}
	if err != nil {
		return fmt.Errorf("读取正文: %w", err)
	}
	if content.ContentHash != job.ContentHash {
		return ErrJobStale
	}

	chunks, err := w.content.ListDocumentChunks(ctx, job.DocumentID)
	if err != nil {
		return fmt.Errorf("读取 chunks: %w", err)
	}
	if len(chunks) == 0 {
		// 理论上 ContentService 不会产生这种状态；清理旧向量后结束，避免留下幽灵数据。
		if err := w.vectors.DeleteByDocument(ctx, job.DocumentID); err != nil {
			return fmt.Errorf("删除空文档向量: %w", err)
		}
		return w.complete(ctx, job)
	}

	points, err := w.embedChunks(ctx, job, chunks)
	if err != nil {
		return err
	}

	// embedding 可能耗时几十秒；写 Qdrant 前再确认正文没有被替换。
	latest, err := w.content.GetDocumentContent(ctx, job.DocumentID)
	if errors.Is(err, knowledge.ErrNotFound) {
		return ErrJobStale
	}
	if err != nil {
		return fmt.Errorf("再次读取正文: %w", err)
	}
	if latest.ContentHash != job.ContentHash {
		return ErrJobStale
	}

	if err := w.vectors.DeleteByDocument(ctx, job.DocumentID); err != nil {
		return fmt.Errorf("删除旧向量: %w", err)
	}
	for start := 0; start < len(points); start += defaultQdrantBatch {
		end := start + defaultQdrantBatch
		if end > len(points) {
			end = len(points)
		}
		if err := w.vectors.Upsert(ctx, points[start:end]); err != nil {
			return fmt.Errorf("写入向量: %w", err)
		}
	}

	if err := w.complete(ctx, job); err != nil {
		return err
	}
	w.logger.Info("embedding 任务完成", "document_id", job.DocumentID, "chunks", len(chunks))
	return nil
}

func (w *Worker) embedChunks(ctx context.Context, job Job, chunks []knowledge.StoredChunk) ([]qdrant.Point, error) {
	points := make([]qdrant.Point, 0, len(chunks))
	for start := 0; start < len(chunks); start += w.batchSize {
		end := start + w.batchSize
		if end > len(chunks) {
			end = len(chunks)
		}
		batch := chunks[start:end]
		texts := make([]string, len(batch))
		for i, chunk := range batch {
			texts[i] = chunk.Content
		}
		vectors, err := w.embedder.Embed(ctx, texts)
		if err != nil {
			return nil, fmt.Errorf("embedding chunk batch [%d:%d]: %w", start, end, err)
		}
		if len(vectors) != len(batch) {
			return nil, fmt.Errorf("embedding chunk batch [%d:%d] 返回 %d 个向量, want %d",
				start, end, len(vectors), len(batch))
		}
		for i, chunk := range batch {
			points = append(points, qdrant.Point{
				ID:     chunk.ID,
				Vector: vectors[i],
				Payload: map[string]any{
					"chunk_id":        chunk.ID.String(),
					"document_id":     job.DocumentID.String(),
					"chunk_index":     chunk.Index,
					"embedding_model": w.model,
					"chunker_version": job.ChunkerVersion,
				},
			})
		}
	}
	return points, nil
}

func (w *Worker) complete(ctx context.Context, job Job) error {
	err := w.jobs.Complete(ctx, job, time.Now().UTC().Truncate(time.Millisecond))
	if errors.Is(err, ErrJobStale) {
		return ErrJobStale
	}
	return err
}

func (w *Worker) fail(ctx context.Context, job Job, cause error) error {
	now := time.Now().UTC().Truncate(time.Millisecond)
	retryAt := now.Add(backoff(job.Attempts))
	err := w.jobs.Fail(ctx, job, cause.Error(), retryAt, w.maxAttempts, now)
	if errors.Is(err, ErrJobStale) {
		w.logger.Warn("失败任务已被新正文覆盖，跳过状态更新", "document_id", job.DocumentID)
		return nil
	}
	return err
}

// Run 循环处理任务；没有任务时按 interval 轮询。
func (w *Worker) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("indexer: interval 必须为正数")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		for {
			processed, err := w.RunOnce(ctx)
			if err != nil {
				w.logger.Error("embedding 任务失败", "error", err)
			}
			if !processed {
				break
			}
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts > 8 {
		attempts = 8
	}
	d := time.Second * time.Duration(1<<uint(attempts-1))
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}
