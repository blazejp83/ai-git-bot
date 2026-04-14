package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

// OpenAIClient implements Client for OpenAI-compatible APIs.
type OpenAIClient struct {
	cfg        Config
	baseURL    string
	apiKey     string
	httpClient *http.Client
	accessToken string
	accountID   string
}

func NewOpenAIClient(baseURL, apiKey string, cfg Config) *OpenAIClient {
	return &OpenAIClient{cfg: cfg, baseURL: baseURL, apiKey: apiKey, httpClient: &http.Client{}}
}

func NewOpenAIClientWithOAuth(baseURL, accessToken, accountID string, cfg Config) *OpenAIClient {
	return &OpenAIClient{cfg: cfg, baseURL: baseURL, accessToken: accessToken, accountID: accountID, httpClient: &http.Client{}}
}

func (c *OpenAIClient) SupportsNativeTools() bool { return true }

// --- Wire types ---

type oaiRequest struct {
	Model     string       `json:"model"`
	Messages  []oaiMsg     `json:"messages"`
	MaxTokens int          `json:"max_completion_tokens"`
	Tools     []oaiTool    `json:"tools,omitempty"`
}

type oaiMsg struct {
	Role       string        `json:"role"`
	Content    any           `json:"content,omitempty"`    // string or nil
	ToolCalls  []oaiToolCall `json:"tool_calls,omitempty"` // assistant tool calls
	ToolCallID string        `json:"tool_call_id,omitempty"` // role=tool
}

type oaiTool struct {
	Type     string      `json:"type"`
	Function oaiFunction `json:"function"`
}

type oaiFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type oaiToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type oaiResponse struct {
	Choices []struct {
		Message struct {
			Role      string        `json:"role"`
			Content   *string       `json:"content"`
			ToolCalls []oaiToolCall `json:"tool_calls"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

// --- Existing methods (unchanged behavior) ---

func (c *OpenAIClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	return reviewDiffCommon(c.cfg, req, func(systemPrompt, model string, maxTokens int, userMsg string) (string, error) {
		messages := []oaiMsg{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMsg},
		}
		return c.doSimpleRequest(ctx, model, maxTokens, messages, "review")
	})
}

func (c *OpenAIClient) Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	return chatCommon(c.cfg, history, userMessage, opts, func(systemPrompt, model string, maxTokens int, messages []Message) (string, error) {
		oaiMsgs := make([]oaiMsg, 0, len(messages)+1)
		oaiMsgs = append(oaiMsgs, oaiMsg{Role: "system", Content: systemPrompt})
		for _, m := range messages {
			oaiMsgs = append(oaiMsgs, oaiMsg{Role: m.Role, Content: m.Content})
		}
		return c.doSimpleRequest(ctx, model, maxTokens, oaiMsgs, "chat")
	})
}

// --- ChatWithTools (new) ---

func (c *OpenAIClient) ChatWithTools(ctx context.Context, messages []ConversationMessage, tools []ToolDef, opts ChatOpts) (*ChatResponse, error) {
	model := resolveModel(opts.ModelOverride, c.cfg.Model)
	prompt := resolvePrompt(opts.SystemPrompt)
	maxTokens := c.cfg.MaxTokens
	if opts.MaxTokensOverride > 0 {
		maxTokens = opts.MaxTokensOverride
	}

	// Build messages
	oaiMsgs := []oaiMsg{{Role: "system", Content: prompt}}
	for _, m := range messages {
		oaiMsgs = append(oaiMsgs, convToOAI(m)...)
	}

	// Build tools
	var oaiTools []oaiTool
	for _, t := range tools {
		oaiTools = append(oaiTools, oaiTool{
			Type: "function",
			Function: oaiFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	reqBody := oaiRequest{
		Model:     model,
		Messages:  oaiMsgs,
		MaxTokens: maxTokens,
		Tools:     oaiTools,
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
		return nil, fmt.Errorf("openai API error (HTTP %d): %s", status, string(respBody))
	}

	var resp oaiResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	result := &ChatResponse{}
	if resp.Usage != nil {
		result.Usage = &TokenUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
		}
		slog.Info("OpenAI tool response", "prompt_tokens", resp.Usage.PromptTokens, "completion_tokens", resp.Usage.CompletionTokens)
	}

	if len(resp.Choices) == 0 {
		result.StopReason = "empty"
		return result, nil
	}

	choice := resp.Choices[0]
	result.StopReason = choice.FinishReason

	if choice.Message.Content != nil && *choice.Message.Content != "" {
		result.Content = append(result.Content, ContentBlock{Type: "text", Text: *choice.Message.Content})
	}

	for _, tc := range choice.Message.ToolCalls {
		var input map[string]any
		json.Unmarshal([]byte(tc.Function.Arguments), &input)
		result.Content = append(result.Content, ContentBlock{
			Type: "tool_use",
			ToolCall: &ToolCall{
				ID:       tc.ID,
				Name:     tc.Function.Name,
				Input:    input,
				RawInput: tc.Function.Arguments,
			},
		})
	}

	return result, nil
}

// --- Helpers ---

func convToOAI(m ConversationMessage) []oaiMsg {
	switch {
	case len(m.ToolResults) > 0:
		// Tool results → one "tool" message per result
		var msgs []oaiMsg
		for _, tr := range m.ToolResults {
			msgs = append(msgs, oaiMsg{
				Role:       "tool",
				Content:    tr.Content,
				ToolCallID: tr.ToolCallID,
			})
		}
		return msgs

	case len(m.ToolCalls) > 0:
		// Assistant with tool calls
		var calls []oaiToolCall
		for _, tc := range m.ToolCalls {
			raw := tc.RawInput
			if raw == "" {
				b, _ := json.Marshal(tc.Input)
				raw = string(b)
			}
			calls = append(calls, oaiToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: tc.Name, Arguments: raw},
			})
		}
		msg := oaiMsg{Role: "assistant", ToolCalls: calls}
		if m.Content != "" {
			msg.Content = m.Content
		}
		return []oaiMsg{msg}

	default:
		return []oaiMsg{{Role: m.Role, Content: m.Content}}
	}
}

func (c *OpenAIClient) doHTTP(ctx context.Context, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.accessToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.accessToken)
		if c.accountID != "" {
			req.Header.Set("ChatGPT-Account-ID", c.accountID)
		}
	} else if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return respBody, resp.StatusCode, nil
}

func (c *OpenAIClient) doSimpleRequest(ctx context.Context, model string, maxTokens int, messages []oaiMsg, logContext string) (string, error) {
	reqBody := oaiRequest{Model: model, Messages: messages, MaxTokens: maxTokens}
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
		return "", fmt.Errorf("openai API error (HTTP %d): %s", status, string(respBody))
	}

	var resp oaiResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == nil {
		return "Unable to generate " + logContext + " - empty response from AI.", nil
	}

	if resp.Usage != nil {
		slog.Info("OpenAI response", "context", logContext,
			"prompt_tokens", resp.Usage.PromptTokens,
			"completion_tokens", resp.Usage.CompletionTokens)
	}

	return *resp.Choices[0].Message.Content, nil
}

func parseRateLimit(status int, body []byte) *RateLimitError {
	err := &RateLimitError{StatusCode: status, Body: string(body)}
	// Try to parse Retry-After from body or use default
	err.RetryAfter = 5 * time.Second
	// Attempt to extract retry-after from error body
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil {
		// Some providers include seconds in the message
		if msg := errResp.Error.Message; msg != "" {
			slog.Warn("Rate limited", "message", msg)
		}
	}
	return err
}

// parseRetryAfterHeader parses the Retry-After header value.
func parseRetryAfterHeader(val string) time.Duration {
	if val == "" {
		return 0
	}
	if secs, err := strconv.Atoi(val); err == nil {
		return time.Duration(secs) * time.Second
	}
	return 0
}
