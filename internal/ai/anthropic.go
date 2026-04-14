package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

type AnthropicClient struct {
	cfg        Config
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewAnthropicClient(baseURL, apiKey string, cfg Config) *AnthropicClient {
	return &AnthropicClient{cfg: cfg, baseURL: baseURL, apiKey: apiKey, httpClient: &http.Client{}}
}

func (c *AnthropicClient) SupportsNativeTools() bool { return true }

// --- Wire types ---

type anthRequest struct {
	Model     string         `json:"model"`
	MaxTokens int            `json:"max_tokens"`
	System    string         `json:"system,omitempty"`
	Messages  []anthMsg      `json:"messages"`
	Tools     []anthTool     `json:"tools,omitempty"`
}

type anthMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthContentBlock
}

type anthContentBlock struct {
	Type       string         `json:"type"`                  // text, tool_use, tool_result
	Text       string         `json:"text,omitempty"`        // type=text
	ID         string         `json:"id,omitempty"`          // type=tool_use
	Name       string         `json:"name,omitempty"`        // type=tool_use
	Input      map[string]any `json:"input,omitempty"`       // type=tool_use
	ToolUseID  string         `json:"tool_use_id,omitempty"` // type=tool_result
	Content    string         `json:"content,omitempty"`     // type=tool_result (can also be array, but we use string)
	IsError    bool           `json:"is_error,omitempty"`    // type=tool_result
}

type anthTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type anthResponse struct {
	Content    []anthRespBlock `json:"content"`
	StopReason string          `json:"stop_reason"`
	Usage      *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

type anthRespBlock struct {
	Type  string         `json:"type"`
	Text  string         `json:"text,omitempty"`
	ID    string         `json:"id,omitempty"`
	Name  string         `json:"name,omitempty"`
	Input map[string]any `json:"input,omitempty"`
}

// --- Existing methods ---

func (c *AnthropicClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	return reviewDiffCommon(c.cfg, req, func(systemPrompt, model string, maxTokens int, userMsg string) (string, error) {
		msgs := []anthMsg{{Role: "user", Content: userMsg}}
		return c.doSimpleRequest(ctx, model, maxTokens, systemPrompt, msgs, "review")
	})
}

func (c *AnthropicClient) Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	return chatCommon(c.cfg, history, userMessage, opts, func(systemPrompt, model string, maxTokens int, messages []Message) (string, error) {
		aMsgs := make([]anthMsg, len(messages))
		for i, m := range messages {
			aMsgs[i] = anthMsg{Role: m.Role, Content: m.Content}
		}
		return c.doSimpleRequest(ctx, model, maxTokens, systemPrompt, aMsgs, "chat")
	})
}

// --- ChatWithTools (new) ---

func (c *AnthropicClient) ChatWithTools(ctx context.Context, messages []ConversationMessage, tools []ToolDef, opts ChatOpts) (*ChatResponse, error) {
	model := resolveModel(opts.ModelOverride, c.cfg.Model)
	prompt := resolvePrompt(opts.SystemPrompt)
	maxTokens := c.cfg.MaxTokens
	if opts.MaxTokensOverride > 0 {
		maxTokens = opts.MaxTokensOverride
	}

	// Build messages
	var aMsgs []anthMsg
	for _, m := range messages {
		aMsgs = append(aMsgs, convToAnth(m)...)
	}

	// Build tools
	var aTools []anthTool
	for _, t := range tools {
		aTools = append(aTools, anthTool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Parameters,
		})
	}

	reqBody := anthRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    prompt,
		Messages:  aMsgs,
		Tools:     aTools,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	respBody, status, err := c.doHTTP(ctx, body)
	if err != nil {
		return nil, err
	}
	if status == http.StatusTooManyRequests {
		return nil, parseRateLimit(status, respBody)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("anthropic API error (HTTP %d): %s", status, string(respBody))
	}

	var resp anthResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	result := &ChatResponse{StopReason: resp.StopReason}
	if resp.Usage != nil {
		result.Usage = &TokenUsage{
			PromptTokens:     resp.Usage.InputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
		}
		slog.Info("Anthropic tool response", "input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens)
	}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			result.Content = append(result.Content, ContentBlock{Type: "text", Text: block.Text})
		case "tool_use":
			raw, _ := json.Marshal(block.Input)
			result.Content = append(result.Content, ContentBlock{
				Type: "tool_use",
				ToolCall: &ToolCall{
					ID:       block.ID,
					Name:     block.Name,
					Input:    block.Input,
					RawInput: string(raw),
				},
			})
		}
	}

	return result, nil
}

// --- Helpers ---

func convToAnth(m ConversationMessage) []anthMsg {
	switch {
	case len(m.ToolResults) > 0:
		// Tool results → user message with tool_result content blocks
		var blocks []anthContentBlock
		for _, tr := range m.ToolResults {
			blocks = append(blocks, anthContentBlock{
				Type:      "tool_result",
				ToolUseID: tr.ToolCallID,
				Content:   tr.Content,
				IsError:   tr.IsError,
			})
		}
		return []anthMsg{{Role: "user", Content: blocks}}

	case len(m.ToolCalls) > 0:
		// Assistant with tool calls
		var blocks []anthContentBlock
		if m.Content != "" {
			blocks = append(blocks, anthContentBlock{Type: "text", Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			blocks = append(blocks, anthContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Name,
				Input: tc.Input,
			})
		}
		return []anthMsg{{Role: "assistant", Content: blocks}}

	default:
		return []anthMsg{{Role: m.Role, Content: m.Content}}
	}
}

func (c *AnthropicClient) doHTTP(ctx context.Context, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}

func (c *AnthropicClient) doSimpleRequest(ctx context.Context, model string, maxTokens int, systemPrompt string, messages []anthMsg, logContext string) (string, error) {
	reqBody := anthRequest{Model: model, MaxTokens: maxTokens, System: systemPrompt, Messages: messages}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	respBody, status, err := c.doHTTP(ctx, body)
	if err != nil {
		return "", err
	}
	if status == http.StatusTooManyRequests {
		return "", parseRateLimit(status, respBody)
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("anthropic API error (HTTP %d): %s", status, string(respBody))
	}

	var resp anthResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	var text string
	for _, block := range resp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	if text == "" {
		return "Unable to generate " + logContext + " - empty response from AI.", nil
	}

	if resp.Usage != nil {
		slog.Info("Anthropic response", "context", logContext,
			"input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens)
	}

	return text, nil
}
