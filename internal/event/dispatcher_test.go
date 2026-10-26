package event

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	events     []Event
	claimErr   error
	requeueErr error
	published  []uuid.UUID
	failed     []Event
	next       []time.Time
}

func (f *fakeStore) RequeueExpired(context.Context) (int64, error) { return 0, f.requeueErr }
func (f *fakeStore) Claim(context.Context, int, time.Duration) ([]Event, error) {
	return f.events, f.claimErr
}
func (f *fakeStore) MarkPublished(_ context.Context, id uuid.UUID) error {
	f.published = append(f.published, id)
	return nil
}
func (f *fakeStore) MarkFailed(_ context.Context, ev Event, next time.Time, _ int) error {
	f.failed = append(f.failed, ev)
	f.next = append(f.next, next)
	return nil
}

type fakePublisher struct{ err error }

func (f *fakePublisher) Publish(context.Context, Event) error { return f.err }
func (f *fakePublisher) Close() error                         { return nil }

func testDispatcher(t *testing.T, store Store, pub Publisher) *Dispatcher {
	t.Helper()
	d, err := NewDispatcher(store, pub, DispatcherOptions{
		BatchSize: 10, Lease: time.Minute, Backoff: time.Second, MaxAttempts: 3,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	return d
}

func TestDispatcherRunOnce(t *testing.T) {
	store := &fakeStore{events: []Event{{ID: uuid.New(), Attempts: 1}}}
	pub := &fakePublisher{}
	d := testDispatcher(t, store, pub)
	n, err := d.RunOnce(context.Background())
	if err != nil || n != 1 {
		t.Fatalf("RunOnce = %d, %v", n, err)
	}
	if len(store.published) != 1 {
		t.Fatalf("published = %d", len(store.published))
	}
}

func TestDispatcherPublishFailure(t *testing.T) {
	store := &fakeStore{events: []Event{{ID: uuid.New(), Attempts: 1}}}
	d := testDispatcher(t, store, &fakePublisher{err: errors.New("kafka down")})
	n, err := d.RunOnce(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("RunOnce = %d, %v", n, err)
	}
	if len(store.failed) != 1 || len(store.next) != 1 {
		t.Fatalf("failed=%d next=%d", len(store.failed), len(store.next))
	}
}

func TestDispatcherErrors(t *testing.T) {
	d := testDispatcher(t, &fakeStore{requeueErr: errors.New("db")}, &fakePublisher{})
	if _, err := d.RunOnce(context.Background()); err == nil {
		t.Fatal("RequeueExpired 错误应返回")
	}
	d = testDispatcher(t, &fakeStore{claimErr: errors.New("db")}, &fakePublisher{})
	if _, err := d.RunOnce(context.Background()); err == nil {
		t.Fatal("Claim 错误应返回")
	}
}

func TestNewDispatcherValidation(t *testing.T) {
	valid := DispatcherOptions{BatchSize: 1, Lease: time.Second, Backoff: time.Second, MaxAttempts: 1}
	tests := []struct {
		name  string
		store Store
		pub   Publisher
		opts  DispatcherOptions
	}{
		{name: "nil store", pub: &fakePublisher{}, opts: valid},
		{name: "nil pub", store: &fakeStore{}, opts: valid},
		{name: "batch", store: &fakeStore{}, pub: &fakePublisher{}, opts: DispatcherOptions{Lease: time.Second, Backoff: time.Second, MaxAttempts: 1}},
		{name: "lease", store: &fakeStore{}, pub: &fakePublisher{}, opts: DispatcherOptions{BatchSize: 1, Backoff: time.Second, MaxAttempts: 1}},
		{name: "attempts", store: &fakeStore{}, pub: &fakePublisher{}, opts: DispatcherOptions{BatchSize: 1, Lease: time.Second, Backoff: time.Second}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewDispatcher(tt.store, tt.pub, tt.opts); err == nil {
				t.Fatal("应当返回错误")
			}
		})
	}
}
