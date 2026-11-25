package ai

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type memRecorder struct {
	mu     sync.Mutex
	usage  []UsageRecord
	errors []ErrorRecord
}

func (m *memRecorder) RecordUsage(r UsageRecord) {
	m.mu.Lock()
	m.usage = append(m.usage, r)
	m.mu.Unlock()
}
func (m *memRecorder) RecordError(r ErrorRecord) {
	m.mu.Lock()
	m.errors = append(m.errors, r)
	m.mu.Unlock()
}

// fakeProvider reports usage the way real providers do, once per request.
type fakeProvider struct {
	requests int
	err      error
}

func (f *fakeProvider) SupportsNativeTools() bool { return true }
func (f *fakeProvider) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	for i := 0; i < f.requests; i++ { // a chunked review makes several requests
		reportUsage(ctx, "claude-opus-5-5", TokenUsage{PromptTokens: 100, CompletionTokens: 10})
	}
	return "ok", f.err
}
func (f *fakeProvider) Chat(ctx context.Context, h []Message, m string, o ChatOpts) (string, error) {
	return "ok", f.err // like a CLI provider: no usage reported
}
func (f *fakeProvider) ChatWithTools(ctx context.Context, m []ConversationMessage, t []ToolDef, o ChatOpts) (*ChatResponse, error) {
	if f.err == nil {
		reportUsage(ctx, "claude-sonnet-5-5", TokenUsage{PromptTokens: 5000, CompletionTokens: 50, CacheReadTokens: 4000})
	}
	return &ChatResponse{}, f.err
}

func TestRecordingClient(t *testing.T) {
	rec := &memRecorder{}
	c := NewRecordingClient(&fakeProvider{requests: 3}, rec, "Anthropic", "anthropic", "claude-opus-5-5")
	ctx := WithRound(WithSession(WithBot(context.Background(), "reviewer"), "o/r#7"), 4)

	c.ReviewDiff(ctx, ReviewRequest{})
	if len(rec.usage) != 3 || rec.usage[0].Kind != "review" || rec.usage[0].Bot != "reviewer" || rec.usage[0].Session != "o/r#7" {
		t.Fatalf("review rows = %+v", rec.usage)
	}
	c.ChatWithTools(ctx, nil, nil, ChatOpts{})
	last := rec.usage[len(rec.usage)-1]
	if last.Kind != "agent" || last.Round != 4 || last.Model != "claude-sonnet-5-5" || last.Usage.CacheReadTokens != 4000 {
		t.Errorf("agent row = %+v", last)
	}
	c.Chat(ctx, nil, "hi", ChatOpts{ModelOverride: "claude-haiku-5-5"})
	last = rec.usage[len(rec.usage)-1]
	if last.Kind != "chat" || last.Model != "claude-haiku-5-5" || last.Usage.PromptTokens != 0 {
		t.Errorf("a call without usage still counts, with zero tokens: %+v", last)
	}

	failing := NewRecordingClient(&fakeProvider{err: errors.New("HTTP 529")}, rec, "Anthropic", "anthropic", "claude-opus-5-5")
	n := len(rec.usage)
	failing.ChatWithTools(ctx, nil, nil, ChatOpts{})
	if len(rec.errors) != 1 || rec.errors[0].Message != "HTTP 529" || rec.errors[0].Round != 4 || len(rec.usage) != n {
		t.Errorf("failed call: errors=%+v, new usage rows=%d", rec.errors, len(rec.usage)-n)
	}

	if NewRecordingClient(&fakeProvider{}, nil, "", "", "") == nil {
		t.Error("nil recorder must return the client itself")
	}
}
