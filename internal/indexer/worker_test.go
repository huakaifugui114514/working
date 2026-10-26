package indexer

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huakaifugui114514/working/internal/embedding"
	"github.com/huakaifugui114514/working/internal/knowledge"
	"github.com/huakaifugui114514/working/internal/qdrant"
)

type fakeJobQueue struct {
	job          *Job
	claimedBy    string
	claimErr     error
	completeErr  error
	completed    []Job
	failed       []Job
	failErr      error
	requeueCalls int
}

func (f *fakeJobQueue) RequeueExpired(context.Context, time.Duration, time.Time) (int64, error) {
	f.requeueCalls++
	return 0, nil
}
func (f *fakeJobQueue) Claim(_ context.Context, workerID string, _ time.Time) (*Job, error) {
	f.claimedBy = workerID
	return f.job, f.claimErr
}
func (f *fakeJobQueue) Complete(_ context.Context, job Job, _ time.Time) error {
	f.completed = append(f.completed, job)
	return f.completeErr
}
func (f *fakeJobQueue) Fail(_ context.Context, job Job, _ string, _ time.Time, _ int, _ time.Time) error {
	f.failed = append(f.failed, job)
	return f.failErr
}

type fakeContent struct {
	content    *knowledge.DocumentContent
	contentErr error
	chunks     []knowledge.StoredChunk
	chunksErr  error
}

func (f *fakeContent) ReplaceDocumentContent(context.Context, uuid.UUID, string, [32]byte, []knowledge.ContentChunk) error {
	panic("unexpected ReplaceDocumentContent")
}
func (f *fakeContent) GetDocumentContent(context.Context, uuid.UUID) (*knowledge.DocumentContent, error) {
	return f.content, f.contentErr
}
func (f *fakeContent) ListDocumentChunks(context.Context, uuid.UUID) ([]knowledge.StoredChunk, error) {
	return f.chunks, f.chunksErr
}

type fakeEmbedder struct {
	embed func(context.Context, []string) ([][]float32, error)
}

func (f *fakeEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return f.embed(ctx, texts)
}

type fakeVectorStore struct {
	deleted []uuid.UUID
	points  []qdrant.Point
	err     error
}

func (f *fakeVectorStore) DeleteByDocument(_ context.Context, id uuid.UUID) error {
	f.deleted = append(f.deleted, id)
	return f.err
}
func (f *fakeVectorStore) Upsert(_ context.Context, points []qdrant.Point) error {
	f.points = append(f.points, points...)
	return f.err
}

func newTestWorker(t *testing.T, jobs JobQueue, content knowledge.ContentRepository, emb embedding.Embedder, vec VectorStore) *Worker {
	t.Helper()
	w, err := NewWorker(WorkerOptions{
		Jobs:        jobs,
		Content:     content,
		Embedder:    emb,
		Vectors:     vec,
		WorkerID:    "test-worker",
		Model:       "bge-m3",
		BatchSize:   2,
		Lease:       30 * time.Second,
		MaxAttempts: 3,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewWorker: %v", err)
	}
	return w
}

func TestWorkerRunOnceSuccess(t *testing.T) {
	docID := uuid.New()
	hash := sha256.Sum256([]byte("正文"))
	chunks := []knowledge.StoredChunk{
		{ID: uuid.New(), Index: 0, Content: "第一段", CharCount: 3},
		{ID: uuid.New(), Index: 1, Content: "第二段", CharCount: 3},
	}
	content := &fakeContent{
		content: &knowledge.DocumentContent{DocumentID: docID, ContentHash: hash, ChunkerVersion: 1},
		chunks:  chunks,
	}
	emb := &fakeEmbedder{embed: func(_ context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range out {
			out[i] = []float32{float32(i), 0.1, 0.2, 0.3}
		}
		return out, nil
	}}
	vec := &fakeVectorStore{}
	jobs := &fakeJobQueue{job: &Job{DocumentID: docID, ContentHash: hash, ChunkerVersion: 1, Attempts: 1}}

	w := newTestWorker(t, jobs, content, emb, vec)
	processed, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !processed {
		t.Fatal("processed = false, want true")
	}
	if len(jobs.completed) != 1 {
		t.Fatalf("completed = %d, want 1", len(jobs.completed))
	}
	if jobs.claimedBy != "test-worker" {
		t.Fatalf("claimedBy = %q, want test-worker", jobs.claimedBy)
	}
	if len(vec.deleted) != 1 || vec.deleted[0] != docID {
		t.Fatalf("deleted = %v", vec.deleted)
	}
	if len(vec.points) != 2 {
		t.Fatalf("points = %d, want 2", len(vec.points))
	}
	if vec.points[0].Payload["document_id"] != docID.String() ||
		vec.points[0].Payload["embedding_model"] != "bge-m3" {
		t.Fatalf("payload = %+v", vec.points[0].Payload)
	}
}

func TestWorkerRunOnceStaleHash(t *testing.T) {
	docID := uuid.New()
	jobHash := sha256.Sum256([]byte("旧正文"))
	newHash := sha256.Sum256([]byte("新正文"))
	content := &fakeContent{
		content: &knowledge.DocumentContent{DocumentID: docID, ContentHash: newHash},
	}
	jobs := &fakeJobQueue{job: &Job{DocumentID: docID, ContentHash: jobHash, Attempts: 1}}
	vec := &fakeVectorStore{}
	w := newTestWorker(t, jobs, content, &fakeEmbedder{}, vec)

	processed, err := w.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if !processed || len(jobs.failed) != 0 || len(vec.deleted) != 0 {
		t.Fatalf("stale job should be skipped: processed=%v failed=%d deleted=%d",
			processed, len(jobs.failed), len(vec.deleted))
	}
}

func TestWorkerRunOnceEmbedFailure(t *testing.T) {
	docID := uuid.New()
	hash := sha256.Sum256([]byte("正文"))
	content := &fakeContent{
		content: &knowledge.DocumentContent{DocumentID: docID, ContentHash: hash},
		chunks:  []knowledge.StoredChunk{{ID: uuid.New(), Index: 0, Content: "第一段"}},
	}
	baseErr := errors.New("ollama down")
	emb := &fakeEmbedder{embed: func(context.Context, []string) ([][]float32, error) {
		return nil, baseErr
	}}
	jobs := &fakeJobQueue{job: &Job{DocumentID: docID, ContentHash: hash, Attempts: 1}}
	w := newTestWorker(t, jobs, content, emb, &fakeVectorStore{})

	processed, err := w.RunOnce(context.Background())
	if !processed || err == nil {
		t.Fatalf("processed=%v err=%v, want processed=true err", processed, err)
	}
	if len(jobs.failed) != 1 {
		t.Fatalf("failed = %d, want 1", len(jobs.failed))
	}
}

func TestWorkerRunOnceSoftDeleted(t *testing.T) {
	docID := uuid.New()
	hash := sha256.Sum256([]byte("正文"))
	content := &fakeContent{contentErr: knowledge.ErrNotFound}
	jobs := &fakeJobQueue{job: &Job{DocumentID: docID, ContentHash: hash, Attempts: 1}}
	vec := &fakeVectorStore{}
	w := newTestWorker(t, jobs, content, &fakeEmbedder{}, vec)

	processed, err := w.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if len(jobs.completed) != 1 || len(vec.deleted) != 0 {
		t.Fatalf("completed=%d deleted=%d", len(jobs.completed), len(vec.deleted))
	}
}

func TestNewWorkerValidation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	valid := WorkerOptions{
		Jobs:        &fakeJobQueue{},
		Content:     &fakeContent{},
		Embedder:    &fakeEmbedder{},
		Vectors:     &fakeVectorStore{},
		WorkerID:    "test-worker",
		Model:       "bge-m3",
		BatchSize:   16,
		Lease:       time.Minute,
		MaxAttempts: 3,
		Logger:      logger,
	}
	tests := []struct {
		name string
		opts WorkerOptions
	}{
		{name: "jobs nil", opts: func() WorkerOptions { o := valid; o.Jobs = nil; return o }()},
		{name: "batch 0", opts: func() WorkerOptions { o := valid; o.BatchSize = 0; return o }()},
		{name: "lease 0", opts: func() WorkerOptions { o := valid; o.Lease = 0; return o }()},
		{name: "max attempts 0", opts: func() WorkerOptions { o := valid; o.MaxAttempts = 0; return o }()},
		{name: "worker id empty", opts: func() WorkerOptions { o := valid; o.WorkerID = ""; return o }()},
		{name: "model empty", opts: func() WorkerOptions { o := valid; o.Model = ""; return o }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewWorker(tt.opts); err == nil {
				t.Fatal("NewWorker 应当返回错误")
			}
		})
	}
}
