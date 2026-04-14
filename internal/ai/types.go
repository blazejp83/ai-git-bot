package ai

import (
	"fmt"
	"strings"
	"time"
)

// ToolDef is a provider-agnostic tool definition.
type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"` // JSON Schema object
}

// ContentBlock represents one block in a structured AI response.
type ContentBlock struct {
	Type     string    // "text" or "tool_use"
	Text     string    // populated when Type == "text"
	ToolCall *ToolCall // populated when Type == "tool_use"
}

// ToolCall represents the AI requesting a tool invocation.
type ToolCall struct {
	ID       string         // provider-assigned ID (for correlation)
	Name     string         // tool name
	Input    map[string]any // parsed arguments
	RawInput string         // original JSON string (for persistence)
}

// ToolResultMessage is what we send back to the AI after executing a tool.
type ToolResultMessage struct {
	ToolCallID string
	ToolName   string
	Content    string
	IsError    bool
}

// ConversationMessage represents a message in a tool-calling conversation.
// It can be a user text, an assistant response (text + tool calls), or tool results.
type ConversationMessage struct {
	Role        string              // "user", "assistant"
	Content     string              // text content
	ToolCalls   []ToolCall          // assistant tool calls (when role == "assistant")
	ToolResults []ToolResultMessage // tool results (appended as user message for Anthropic)
}

// ChatResponse is the structured return from ChatWithTools.
type ChatResponse struct {
	Content    []ContentBlock
	StopReason string // "end_turn", "tool_use", "max_tokens", "stop"
	Usage      *TokenUsage
}

// Text returns concatenated text from all text blocks.
func (r *ChatResponse) Text() string {
	var parts []string
	for _, b := range r.Content {
		if b.Type == "text" && b.Text != "" {
			parts = append(parts, b.Text)
		}
	}
	return strings.Join(parts, "")
}

// ToolCalls returns all tool_use blocks.
func (r *ChatResponse) ToolCalls() []ToolCall {
	var calls []ToolCall
	for _, b := range r.Content {
		if b.Type == "tool_use" && b.ToolCall != nil {
			calls = append(calls, *b.ToolCall)
		}
	}
	return calls
}

// HasToolCalls returns true if the response contains any tool_use blocks.
func (r *ChatResponse) HasToolCalls() bool {
	for _, b := range r.Content {
		if b.Type == "tool_use" {
			return true
		}
	}
	return false
}

// RateLimitError is returned on temporary 429s (retry in seconds).
type RateLimitError struct {
	StatusCode int
	RetryAfter time.Duration
	Body       string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited (HTTP %d), retry after %v", e.StatusCode, e.RetryAfter)
}

// UsageLimitError is returned when the user has exhausted their usage cap
// (e.g. ChatGPT Plus 5-hour limit). This is NOT retryable with backoff —
// the agent must stop and inform the user when the limit resets.
type UsageLimitError struct {
	ErrorType  string    // "usage_limit_reached", "insufficient_quota", "usage_not_included"
	Message    string    // human-readable message
	ResetsAt   time.Time // when the limit resets (zero if unknown)
	PlanType   string    // "free", "plus", "pro", "team", etc.
}

func (e *UsageLimitError) Error() string {
	if !e.ResetsAt.IsZero() {
		return fmt.Sprintf("usage limit reached (%s), resets at %s", e.ErrorType, e.ResetsAt.Format("Jan 2 15:04 MST"))
	}
	return fmt.Sprintf("usage limit reached (%s): %s", e.ErrorType, e.Message)
}

// IsRetryable returns false — usage limits require waiting for the reset window.
func (e *UsageLimitError) IsRetryable() bool { return false }
