package event

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// DispatcherOptions 是发布循环参数。
type DispatcherOptions struct {
	BatchSize   int
	Lease       time.Duration
	Backoff     time.Duration
	MaxAttempts int
	Logger      *slog.Logger
}

// Dispatcher 从 outbox 抢事件并发布到 Kafka。
type Dispatcher struct {
	store Store
	pub   Publisher
	opts  DispatcherOptions
}

// NewDispatcher 校验参数。
func NewDispatcher(store Store, pub Publisher, opts DispatcherOptions) (*Dispatcher, error) {
	if store == nil || pub == nil {
		return nil, fmt.Errorf("event: store/publisher 不能为空")
	}
	if opts.BatchSize < 1 {
		return nil, fmt.Errorf("event: batch_size 必须 ≥ 1")
	}
	if opts.Lease <= 0 || opts.Backoff <= 0 {
		return nil, fmt.Errorf("event: lease/backoff 必须为正数")
	}
	if opts.MaxAttempts < 1 {
		return nil, fmt.Errorf("event: max_attempts 必须 ≥ 1")
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Dispatcher{store: store, pub: pub, opts: opts}, nil
}

// RunOnce 处理一批事件，返回成功发布数。
func (d *Dispatcher) RunOnce(ctx context.Context) (int, error) {
	if _, err := d.store.RequeueExpired(ctx); err != nil {
		return 0, err
	}
	events, err := d.store.Claim(ctx, d.opts.BatchSize, d.opts.Lease)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, ev := range events {
		if err := d.pub.Publish(ctx, ev); err != nil {
			next := time.Now().UTC().Add(d.opts.Backoff * time.Duration(ev.Attempts))
			if markErr := d.store.MarkFailed(ctx, ev, next, d.opts.MaxAttempts); markErr != nil {
				d.opts.Logger.ErrorContext(ctx, "事件发布失败且标记失败", "event_id", ev.ID, "error", err, "mark_error", markErr)
			} else {
				d.opts.Logger.WarnContext(ctx, "事件发布失败，已安排重试", "event_id", ev.ID, "attempts", ev.Attempts, "error", err)
			}
			continue
		}
		if err := d.store.MarkPublished(ctx, ev.ID); err != nil {
			d.opts.Logger.ErrorContext(ctx, "事件已发布但标记失败", "event_id", ev.ID, "error", err)
			continue
		}
		published++
	}
	return published, nil
}

// Run 常驻循环，直到 ctx 取消。
func (d *Dispatcher) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("event: interval 必须为正数")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := d.RunOnce(ctx); err != nil && ctx.Err() == nil {
			d.opts.Logger.ErrorContext(ctx, "事件 dispatcher 失败", "error", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
