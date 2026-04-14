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

// RateLimitError is returned when the AI provider rate-limits us.
type RateLimitError struct {
	StatusCode int
	RetryAfter time.Duration
	Body       string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("rate limited (HTTP %d), retry after %v", e.StatusCode, e.RetryAfter)
}
