package agent

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStream struct {
	deltas []StreamDelta
	idx    int
	errAt  int
	err    error
	closed bool
}

func (s *fakeStream) Recv() (StreamDelta, error) {
	if s.err != nil && s.idx == s.errAt {
		return StreamDelta{}, s.err
	}
	if s.idx >= len(s.deltas) {
		return StreamDelta{Done: true}, nil
	}
	d := s.deltas[s.idx]
	s.idx++
	return d, nil
}
func (s *fakeStream) Close() error { s.closed = true; return nil }

func TestServiceChatStream(t *testing.T) {
	chunks := []RetrievedChunk{{ChunkID: uuid.New(), DocumentID: uuid.New(), Content: "资料"}}
	retriever := &fakeRetriever{retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
		return chunks, nil
	}}
	stream := &fakeStream{deltas: []StreamDelta{{Content: "你"}, {Content: "好"}}}
	llm := &fakeLLM{
		stream: func(context.Context, ChatRequest) (ChatStream, error) { return stream, nil },
	}
	svc := NewService(retriever, llm, &fakeCollectionLookup{}, 5, 1024)

	ch, err := svc.ChatStream(context.Background(), "问题", nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var got string
	var done bool
	var citations int
	for ev := range ch {
		if ev.Err != nil {
			t.Fatalf("event err: %v", ev.Err)
		}
		got += ev.Delta
		if ev.Done {
			done = true
			citations = len(ev.Citations)
		}
	}
	if got != "你好" || !done || citations != 1 {
		t.Fatalf("got=%q done=%v citations=%d", got, done, citations)
	}
	if !stream.closed {
		t.Fatal("stream 未关闭")
	}
}

func TestServiceChatStreamNoContext(t *testing.T) {
	retriever := &fakeRetriever{retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
		return nil, nil
	}}
	// fakeLLM 没有 stream 函数；0 条结果时不应调用 LLM。
	svc := NewService(retriever, &fakeLLM{}, &fakeCollectionLookup{}, 5, 1024)
	ch, err := svc.ChatStream(context.Background(), "问题", nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var answer string
	var done bool
	for ev := range ch {
		answer += ev.Delta
		if ev.Done {
			done = true
			if ev.Citations == nil || len(ev.Citations) != 0 {
				t.Fatalf("citations = %#v", ev.Citations)
			}
		}
	}
	if answer != NoContextAnswer || !done {
		t.Fatalf("answer=%q done=%v", answer, done)
	}
}

func TestServiceChatStreamFallback(t *testing.T) {
	chunks := []RetrievedChunk{{ChunkID: uuid.New(), DocumentID: uuid.New(), Content: "资料"}}
	retriever := &fakeRetriever{retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
		return chunks, nil
	}}
	// onlyChat 只实现 LLMClient，不实现 StreamingLLMClient。
	onlyChat := &onlyChatLLM{answer: "一次性答案"}
	svc := NewService(retriever, onlyChat, &fakeCollectionLookup{}, 5, 1024)

	ch, err := svc.ChatStream(context.Background(), "问题", nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var answer string
	var done bool
	for ev := range ch {
		if ev.Err != nil {
			t.Fatalf("event err: %v", ev.Err)
		}
		answer += ev.Delta
		done = done || ev.Done
	}
	if answer != "一次性答案" || !done {
		t.Fatalf("answer=%q done=%v", answer, done)
	}
}

func TestServiceChatStreamRuntimeError(t *testing.T) {
	chunks := []RetrievedChunk{{ChunkID: uuid.New(), DocumentID: uuid.New(), Content: "资料"}}
	retriever := &fakeRetriever{retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
		return chunks, nil
	}}
	baseErr := errors.New("stream broken")
	llm := &fakeLLM{stream: func(context.Context, ChatRequest) (ChatStream, error) {
		return &fakeStream{deltas: []StreamDelta{{Content: "部分"}}, errAt: 1, err: baseErr}, nil
	}}
	svc := NewService(retriever, llm, &fakeCollectionLookup{}, 5, 1024)
	ch, err := svc.ChatStream(context.Background(), "问题", nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	var gotErr error
	for ev := range ch {
		if ev.Err != nil {
			gotErr = ev.Err
		}
	}
	if !errors.Is(gotErr, baseErr) {
		t.Fatalf("err = %v, want baseErr", gotErr)
	}
}

func TestServiceChatStreamCancel(t *testing.T) {
	chunks := []RetrievedChunk{{ChunkID: uuid.New(), DocumentID: uuid.New(), Content: "资料"}}
	retriever := &fakeRetriever{retrieve: func(context.Context, string, *uuid.UUID, int) ([]RetrievedChunk, error) {
		return chunks, nil
	}}
	llm := &fakeLLM{stream: func(context.Context, ChatRequest) (ChatStream, error) {
		return &blockingStream{}, nil
	}}
	svc := NewService(retriever, llm, &fakeCollectionLookup{}, 5, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := svc.ChatStream(ctx, "问题", nil)
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	cancel()
	select {
	case _, ok := <-ch:
		if ok {
			// 可能先收到 close 前已排队的事件；继续读到关闭。
			for range ch {
			}
		}
	case <-time.After(time.Second):
		t.Fatal("取消后 channel 未关闭")
	}
}

type onlyChatLLM struct{ answer string }

func (f *onlyChatLLM) Chat(context.Context, ChatRequest) (*ChatResponse, error) {
	return &ChatResponse{Content: f.answer}, nil
}

type blockingStream struct{}

func (s *blockingStream) Recv() (StreamDelta, error) {
	time.Sleep(10 * time.Millisecond)
	return StreamDelta{}, io.EOF
}
func (s *blockingStream) Close() error { return nil }
