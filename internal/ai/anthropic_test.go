package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAnthropicThinkingReplayAndAdaptive(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			t.Errorf("missing api key header")
		}
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(raw, &m)
		bodies = append(bodies, m)
		io.WriteString(w, `{"content":[
			{"type":"thinking","thinking":"","signature":"sig123"},
			{"type":"tool_use","id":"tu_1","name":"list_files","input":{}}
		],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	c := NewAnthropicClient(srv.URL, "test-key", Config{Model: "claude-opus-5-5", MaxTokens: 4096, ExtendedThinking: true, ThinkingBudget: 10000})
	msgs := []ConversationMessage{{Role: "user", Content: "hi"}}
	resp, err := c.ChatWithTools(context.Background(), msgs, nil, ChatOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(resp.ThinkingBlocks()); got != 1 {
		t.Fatalf("thinking blocks = %d, want 1", got)
	}

	// Adaptive thinking, no budget_tokens, on current models.
	th := bodies[0]["thinking"].(map[string]any)
	if th["type"] != "adaptive" || th["budget_tokens"] != nil {
		t.Errorf("thinking = %v, want adaptive without budget", th)
	}

	// Second turn must replay the thinking block and send input:{} for the tool call.
	msgs = append(msgs,
		ConversationMessage{Role: "assistant", ToolCalls: resp.ToolCalls(), Thinking: resp.ThinkingBlocks()},
		ConversationMessage{Role: "user", ToolResults: []ToolResultMessage{{ToolCallID: "tu_1", Content: "a.go"}}},
	)
	if _, err := c.ChatWithTools(context.Background(), msgs, nil, ChatOpts{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(bodies[1]["messages"])
	s := string(raw)
	for _, want := range []string{`"type":"thinking"`, `"signature":"sig123"`, `"thinking":""`, `"input":{}`} {
		if !strings.Contains(s, want) {
			t.Errorf("second request missing %s: %s", want, s)
		}
	}
	if strings.Index(s, `"type":"thinking"`) > strings.Index(s, `"type":"tool_use"`) {
		t.Errorf("thinking block must precede tool_use: %s", s)
	}
}

func TestAnthropicLegacyThinkingModel(t *testing.T) {
	c := NewAnthropicClient("", "", Config{ExtendedThinking: true, ThinkingBudget: 8000})
	req := anthRequest{MaxTokens: 4096}
	c.applyThinking(&req, "claude-sonnet-4-5")
	if req.Thinking == nil || req.Thinking.Type != "enabled" || req.Thinking.BudgetTokens != 8000 || req.MaxTokens <= 8000 {
		t.Errorf("legacy thinking = %+v max=%d", req.Thinking, req.MaxTokens)
	}
}

func TestAnthropicPromptCaching(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(raw, &m)
		bodies = append(bodies, m)
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn",
			"usage":{"input_tokens":10,"output_tokens":2,"cache_read_input_tokens":900,"cache_creation_input_tokens":50}}`)
	}))
	defer srv.Close()

	c := NewAnthropicClient(srv.URL, "k", Config{Model: "claude-opus-5-5", MaxTokens: 1000})
	resp, err := c.ChatWithTools(context.Background(), []ConversationMessage{{Role: "user", Content: "hi"}}, nil, ChatOpts{SystemPrompt: "You are a reviewer."})
	if err != nil {
		t.Fatal(err)
	}
	b := bodies[0]
	if cc, _ := b["cache_control"].(map[string]any); cc["type"] != "ephemeral" {
		t.Errorf("top-level automatic caching missing: %v", b["cache_control"])
	}
	sys, _ := b["system"].([]any)
	if len(sys) != 1 {
		t.Fatalf("system = %v", b["system"])
	}
	blk := sys[0].(map[string]any)
	if blk["text"] != "You are a reviewer." || blk["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Errorf("system block = %v", blk)
	}
	u := resp.Usage
	if u.CacheReadTokens != 900 || u.CacheWriteTokens != 50 || u.PromptTokens != 960 {
		t.Errorf("usage = %+v", u)
	}
}

func TestAnthropicAutoCacheFallback(t *testing.T) {
	var withTopLevel, calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(raw, &m)
		if _, ok := m["cache_control"]; ok {
			withTopLevel++
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"type":"error","error":{"type":"invalid_request_error","message":"cache_control: Extra inputs are not permitted"}}`)
			return
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
	}))
	defer srv.Close()

	c := NewAnthropicClient(srv.URL, "k", Config{Model: "claude-opus-5-5", MaxTokens: 1000})
	for i := 0; i < 2; i++ {
		out, err := c.Chat(context.Background(), nil, "hi", ChatOpts{SystemPrompt: "s"})
		if err != nil || out != "ok" {
			t.Fatalf("call %d: %q %v", i, out, err)
		}
	}
	if withTopLevel != 1 || calls != 3 {
		t.Errorf("top-level attempts = %d, calls = %d; want 1 and 3 (one retry, then stays off)", withTopLevel, calls)
	}
}
