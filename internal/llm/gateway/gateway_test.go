package gateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/huakaifugui114514/working/internal/agent"
)

type fakeLLM struct {
	name string
	fail bool
	text string
}

func (f *fakeLLM) Chat(context.Context, agent.ChatRequest) (*agent.ChatResponse, error) {
	if f.fail {
		return nil, errors.New(f.name + " down")
	}
	return &agent.ChatResponse{Content: f.text, Usage: agent.TokenUsage{PromptTokens: 10, CompletionTokens: 5}}, nil
}

type fakeStreamer struct{ *fakeLLM }

func (f *fakeStreamer) ChatStream(context.Context, agent.ChatRequest) (agent.ChatStream, error) {
	if f.fail {
		return nil, errors.New("stream down")
	}
	return &fakeStream{deltas: []agent.StreamDelta{{Content: "ok"}, {Done: true}}}, nil
}

type fakeStream struct {
	deltas []agent.StreamDelta
	i      int
}

func (f *fakeStream) Recv() (agent.StreamDelta, error) {
	if f.i >= len(f.deltas) {
		return agent.StreamDelta{Done: true}, nil
	}
	d := f.deltas[f.i]
	f.i++
	return d, nil
}
func (f *fakeStream) Close() error { return nil }

func testGateway(t *testing.T, providers ...Provider) *Gateway {
	t.Helper()
	g, err := New(providers, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return g
}

func TestGatewayFallback(t *testing.T) {
	g := testGateway(t,
		Provider{Name: "primary", Client: &fakeLLM{name: "primary", fail: true}},
		Provider{Name: "backup", Client: &fakeLLM{name: "backup", text: "answer"}},
	)
	resp, err := g.Chat(context.Background(), agent.ChatRequest{})
	if err != nil || resp.Content != "answer" {
		t.Fatalf("resp=%+v err=%v", resp, err)
	}
	stats := g.Stats()
	if stats["primary"].Errors != 1 || stats["backup"].Requests != 1 || stats["backup"].PromptTokens != 10 {
		t.Fatalf("stats = %+v", stats)
	}
}

func TestGatewayAllFail(t *testing.T) {
	g := testGateway(t,
		Provider{Name: "a", Client: &fakeLLM{name: "a", fail: true}},
		Provider{Name: "b", Client: &fakeLLM{name: "b", fail: true}},
	)
	if _, err := g.Chat(context.Background(), agent.ChatRequest{}); err == nil {
		t.Fatal("全部失败应返回错误")
	}
}

func TestGatewayStreamFallback(t *testing.T) {
	g := testGateway(t,
		Provider{Name: "primary", Client: &fakeStreamer{&fakeLLM{name: "primary", fail: true}}},
		Provider{Name: "backup", Client: &fakeStreamer{&fakeLLM{name: "backup"}}},
	)
	stream, err := g.ChatStream(context.Background(), agent.ChatRequest{})
	if err != nil {
		t.Fatalf("ChatStream: %v", err)
	}
	d, err := stream.Recv()
	if err != nil || d.Content != "ok" {
		t.Fatalf("delta=%+v err=%v", d, err)
	}
	_ = stream.Close()
}

func TestNewValidation(t *testing.T) {
	tests := []struct {
		name      string
		providers []Provider
	}{
		{name: "empty"},
		{name: "no name", providers: []Provider{{Client: &fakeLLM{}}}},
		{name: "no client", providers: []Provider{{Name: "a"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.providers, nil); err == nil {
				t.Fatal("应当返回错误")
			}
		})
	}
}

var _ = strings.Contains
