package ai

import "context"

// Message represents a single message in a conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ReviewRequest holds parameters for a code review.
type ReviewRequest struct {
	PRTitle       string
	PRBody        string
	Diff          string
	SystemPrompt  string
	ModelOverride string
}

// ChatOpts holds optional overrides for chat requests.
type ChatOpts struct {
	SystemPrompt     string
	ModelOverride    string
	MaxTokensOverride int
}

// TokenUsage reports prompt and completion token counts.
type TokenUsage struct {
	PromptTokens     int
	CompletionTokens int
}

// Client is the provider-agnostic interface for AI-powered code review and chat.
type Client interface {
	ReviewDiff(ctx context.Context, req ReviewRequest) (string, error)
	Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error)
}
