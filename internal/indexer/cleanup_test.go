package indexer

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeCleanupQueue struct {
	job         *CleanupJob
	completed   []CleanupJob
	failed      []CleanupJob
	claimedBy   string
	completeErr error
}

func (f *fakeCleanupQueue) RequeueExpired(context.Context, time.Duration, time.Time) (int64, error) {
	return 0, nil
}
func (f *fakeCleanupQueue) Claim(_ context.Context, workerID string, _ time.Time) (*CleanupJob, error) {
	f.claimedBy = workerID
	return f.job, nil
}
func (f *fakeCleanupQueue) Complete(_ context.Context, job CleanupJob, _ time.Time) error {
	f.completed = append(f.completed, job)
	return f.completeErr
}
func (f *fakeCleanupQueue) Fail(_ context.Context, job CleanupJob, _ string, _ time.Time, _ int, _ time.Time) error {
	f.failed = append(f.failed, job)
	return nil
}

func newTestCleanupWorker(t *testing.T, jobs CleanupQueue, vectors VectorStore) *CleanupWorker {
	t.Helper()
	w, err := NewCleanupWorker(CleanupWorkerOptions{
		Jobs:        jobs,
		Vectors:     vectors,
		WorkerID:    "cleaner",
		Lease:       time.Minute,
		MaxAttempts: 3,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewCleanupWorker: %v", err)
	}
	return w
}

func TestCleanupWorkerSuccess(t *testing.T) {
	docID := uuid.New()
	vec := &fakeVectorStore{}
	jobs := &fakeCleanupQueue{job: &CleanupJob{DocumentID: docID, Attempts: 1}}
	w := newTestCleanupWorker(t, jobs, vec)

	processed, err := w.RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if len(vec.deleted) != 1 || vec.deleted[0] != docID {
		t.Fatalf("deleted = %v", vec.deleted)
	}
	if len(jobs.completed) != 1 || jobs.claimedBy != "cleaner" {
		t.Fatalf("completed=%d claimedBy=%q", len(jobs.completed), jobs.claimedBy)
	}
}

func TestCleanupWorkerFailure(t *testing.T) {
	docID := uuid.New()
	vec := &fakeVectorStore{err: errors.New("qdrant down")}
	jobs := &fakeCleanupQueue{job: &CleanupJob{DocumentID: docID, Attempts: 1}}
	w := newTestCleanupWorker(t, jobs, vec)

	processed, err := w.RunOnce(context.Background())
	if !processed || err == nil {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	if len(jobs.failed) != 1 || len(jobs.completed) != 0 {
		t.Fatalf("failed=%d completed=%d", len(jobs.failed), len(jobs.completed))
	}
}

func TestCleanupWorkerNoJob(t *testing.T) {
	w := newTestCleanupWorker(t, &fakeCleanupQueue{}, &fakeVectorStore{})
	processed, err := w.RunOnce(context.Background())
	if err != nil || processed {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
}

func TestNewCleanupWorkerValidation(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	valid := CleanupWorkerOptions{
		Jobs:        &fakeCleanupQueue{},
		Vectors:     &fakeVectorStore{},
		WorkerID:    "cleaner",
		Lease:       time.Minute,
		MaxAttempts: 3,
		Logger:      logger,
	}
	tests := []struct {
		name string
		opts CleanupWorkerOptions
	}{
		{name: "jobs nil", opts: func() CleanupWorkerOptions { o := valid; o.Jobs = nil; return o }()},
		{name: "vectors nil", opts: func() CleanupWorkerOptions { o := valid; o.Vectors = nil; return o }()},
		{name: "worker empty", opts: func() CleanupWorkerOptions { o := valid; o.WorkerID = ""; return o }()},
		{name: "lease 0", opts: func() CleanupWorkerOptions { o := valid; o.Lease = 0; return o }()},
		{name: "attempts 0", opts: func() CleanupWorkerOptions { o := valid; o.MaxAttempts = 0; return o }()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewCleanupWorker(tt.opts); err == nil {
				t.Fatal("NewCleanupWorker 应当返回错误")
			}
		})
	}
}
