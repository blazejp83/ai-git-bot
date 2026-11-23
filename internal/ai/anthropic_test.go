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
