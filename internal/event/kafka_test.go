package event

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
)

type fakeWriter struct {
	msgs   []kafka.Message
	err    error
	closed bool
}

func (f *fakeWriter) WriteMessages(_ context.Context, msgs ...kafka.Message) error {
	f.msgs = append(f.msgs, msgs...)
	return f.err
}
func (f *fakeWriter) Close() error { f.closed = true; return nil }

func TestKafkaPublisher(t *testing.T) {
	fw := &fakeWriter{}
	pub := &KafkaPublisher{writer: fw}
	ev := Event{ID: uuid.New(), DocumentID: uuid.New(), Type: "x", Payload: json.RawMessage(`{"a":1}`)}
	if err := pub.Publish(context.Background(), ev); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if len(fw.msgs) != 1 || string(fw.msgs[0].Value) == "" || string(fw.msgs[0].Key) == "" {
		t.Fatalf("msgs = %+v", fw.msgs)
	}
	fw.err = errors.New("broker down")
	if err := pub.Publish(context.Background(), ev); err == nil {
		t.Fatal("writer 错误应返回")
	}
	if err := pub.Close(); err != nil || !fw.closed {
		t.Fatalf("Close = %v closed=%v", err, fw.closed)
	}
}

func TestNewKafkaPublisherValidation(t *testing.T) {
	tests := []struct {
		brokers []string
		topic   string
		timeout time.Duration
	}{
		{nil, "t", time.Second},
		{[]string{"b"}, "", time.Second},
		{[]string{"b"}, "t", 0},
	}
	for _, tt := range tests {
		if _, err := NewKafkaPublisher(tt.brokers, tt.topic, tt.timeout); err == nil {
			t.Fatal("应当返回错误")
		}
	}
}

type fakeReader struct {
	msgs      []kafka.Message
	idx       int
	fetchErr  error
	committed []kafka.Message
	closed    bool
	cancel    context.CancelFunc
}

func (f *fakeReader) FetchMessage(context.Context) (kafka.Message, error) {
	if f.fetchErr != nil {
		return kafka.Message{}, f.fetchErr
	}
	if f.idx >= len(f.msgs) {
		if f.cancel != nil {
			f.cancel()
		}
		return kafka.Message{}, context.Canceled
	}
	m := f.msgs[f.idx]
	f.idx++
	return m, nil
}
func (f *fakeReader) CommitMessages(_ context.Context, msgs ...kafka.Message) error {
	f.committed = append(f.committed, msgs...)
	return nil
}
func (f *fakeReader) Close() error { f.closed = true; return nil }

func TestConsumerRun(t *testing.T) {
	valid, _ := json.Marshal(Event{ID: uuid.New(), DocumentID: uuid.New()})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &fakeReader{cancel: cancel, msgs: []kafka.Message{
		{Value: valid},
		{Value: []byte("bad-json")},
		{Value: valid},
	}}
	seen := 0
	c := &Consumer{reader: reader, handler: func(_ context.Context, _ Event) error {
		seen++
		if seen == 2 {
			return errors.New("retry")
		}
		return nil
	}}
	if err := c.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if seen != 2 {
		t.Fatalf("seen = %d", seen)
	}
	if len(reader.committed) != 2 {
		t.Fatalf("committed = %d, want 2（有效+毒消息；失败的不提交）", len(reader.committed))
	}
	if !reader.closed {
		t.Fatal("reader 未关闭")
	}
}

func TestConsumerFetchError(t *testing.T) {
	c := &Consumer{reader: &fakeReader{fetchErr: errors.New("boom")}, handler: func(context.Context, Event) error { return nil }}
	if err := c.Run(context.Background()); err == nil {
		t.Fatal("fetch 错误应返回")
	}
}

func TestNewConsumerValidation(t *testing.T) {
	if _, err := NewConsumer(nil, "t", "g", func(context.Context, Event) error { return nil }); err == nil {
		t.Fatal("空 brokers 应返回错误")
	}
	if _, err := NewConsumer([]string{"b"}, "t", "g", nil); err == nil {
		t.Fatal("nil handler 应返回错误")
	}
}
