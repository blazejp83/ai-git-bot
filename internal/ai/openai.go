package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
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
	Model            string       `json:"model"`
	Messages         []oaiMsg     `json:"messages"`
	MaxTokens        int          `json:"max_completion_tokens"`
	Tools            []oaiTool    `json:"tools,omitempty"`
	ReasoningEffort  string       `json:"reasoning_effort,omitempty"` // "low", "medium", "high" for o-series models
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
	// OAuth tokens require the Responses API
	if c.accessToken != "" {
		return c.chatViaResponses(ctx, history, userMessage, opts)
	}
	return chatCommon(c.cfg, history, userMessage, opts, func(systemPrompt, model string, maxTokens int, messages []Message) (string, error) {
		oaiMsgs := make([]oaiMsg, 0, len(messages)+1)
		oaiMsgs = append(oaiMsgs, oaiMsg{Role: "system", Content: systemPrompt})
		for _, m := range messages {
			oaiMsgs = append(oaiMsgs, oaiMsg{Role: m.Role, Content: m.Content})
		}
		return c.doSimpleRequest(ctx, model, maxTokens, oaiMsgs, "chat")
	})
}

// chatViaResponses uses the /v1/responses endpoint for OAuth-authenticated calls.
func (c *OpenAIClient) chatViaResponses(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	model := resolveModel(opts.ModelOverride, c.cfg.Model)
	prompt := resolvePrompt(opts.SystemPrompt)
	maxTokens := c.cfg.MaxTokens
	if opts.MaxTokensOverride > 0 {
		maxTokens = opts.MaxTokensOverride
	}

	// Build input array from history + new message
	var input []map[string]any
	for _, m := range history {
		input = append(input, map[string]any{"role": m.Role, "content": m.Content})
	}
	input = append(input, map[string]any{"role": "user", "content": userMessage})

	reqBody := map[string]any{
		"model":             model,
		"instructions":      prompt,
		"input":             input,
		"max_output_tokens": maxTokens,
		"stream":            false,
		"store":             false,
	}

	body, _ := json.Marshal(reqBody)
	hr, err := c.doHTTPResponses(ctx, body)
	if err != nil {
		return "", err
	}
	if hr.status == http.StatusTooManyRequests {
		return "", parse429Response(hr.status, hr.body)
	}
	if hr.status != http.StatusOK {
		return "", fmt.Errorf("openai responses API error (HTTP %d): %s", hr.status, string(hr.body))
	}

	// Parse responses API format
	var resp struct {
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
		Usage *struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(hr.body, &resp); err != nil {
		return "", fmt.Errorf("parse responses: %w", err)
	}

	if resp.Usage != nil {
		slog.Info("OpenAI Responses API", "input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens)
	}

	// Extract text from output
	var text string
	for _, out := range resp.Output {
		if out.Type == "message" {
			for _, c := range out.Content {
				if c.Type == "output_text" {
					text += c.Text
				}
			}
		}
	}

	if text == "" {
		return "Unable to generate response - empty output from AI.", nil
	}
	return text, nil
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

	// Enable reasoning for o-series models when extended thinking is on
	if c.cfg.ExtendedThinking {
		reqBody.ReasoningEffort = "high"
		slog.Info("Reasoning effort enabled", "level", "high")
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	hr, err := c.doHTTPFull(ctx, body)
	if err != nil {
		return nil, err
	}
	if hr.status == http.StatusTooManyRequests {
		return nil, parse429Response(hr.status, hr.body)
	}
	if hr.status != http.StatusOK {
		return nil, fmt.Errorf("openai API error (HTTP %d): %s", hr.status, string(hr.body))
	}

	var resp oaiResponse
	if err := json.Unmarshal(hr.body, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	result := &ChatResponse{
		RateLimit: parseRateLimitHeaders(hr.headers),
	}
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

type httpResult struct {
	body    []byte
	status  int
	headers http.Header
}

func (c *OpenAIClient) doHTTPFull(ctx context.Context, body []byte) (*httpResult, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
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
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return &httpResult{body: respBody, status: resp.StatusCode, headers: resp.Header}, nil
}

// ChatGPT OAuth endpoint — matches what Codex CLI uses
const chatGPTResponsesURL = "https://chatgpt.com/backend-api/codex/responses"

func (c *OpenAIClient) doHTTPResponses(ctx context.Context, body []byte) (*httpResult, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", chatGPTResponsesURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("originator", "codex-tui")
	if c.accountID != "" {
		req.Header.Set("chatgpt-account-id", c.accountID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai responses request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return &httpResult{body: respBody, status: resp.StatusCode, headers: resp.Header}, nil
}

// Backward-compat wrapper
func (c *OpenAIClient) doHTTP(ctx context.Context, body []byte) ([]byte, int, error) {
	r, err := c.doHTTPFull(ctx, body)
	if err != nil {
		return nil, 0, err
	}
	return r.body, r.status, nil
}

// parseRateLimitHeaders extracts usage percentage from response headers.
func parseRateLimitHeaders(headers http.Header) *RateLimitSnapshot {
	usedStr := headers.Get("x-codex-primary-used-percent")
	if usedStr == "" {
		// Try without prefix
		usedStr = headers.Get("x-ratelimit-used-percent")
	}
	if usedStr == "" {
		return nil
	}

	var used float64
	fmt.Sscanf(usedStr, "%f", &used)
	if used == 0 {
		return nil
	}

	snap := &RateLimitSnapshot{
		UsedPercent: used,
		LimitName:   headers.Get("x-codex-limit-name"),
	}

	if resetStr := headers.Get("x-codex-primary-reset-at"); resetStr != "" {
		var resetUnix int64
		fmt.Sscanf(resetStr, "%d", &resetUnix)
		if resetUnix > 0 {
			snap.ResetsAt = time.Unix(resetUnix, 0)
		}
	}

	return snap
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
		return "", parse429Response(status, respBody)
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

// parse429Response parses a 429 response and returns either a retryable
// RateLimitError or a non-retryable UsageLimitError.
func parse429Response(status int, body []byte) error {
	var errResp struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
		// OpenAI usage limit response fields (top-level for some endpoints)
		ErrorType string `json:"error_type"`
		ResetAt   int64  `json:"resets_at"`
		PlanType  string `json:"plan_type"`
	}
	json.Unmarshal(body, &errResp)

	code := errResp.Error.Code
	if code == "" {
		code = errResp.ErrorType
	}
	msg := errResp.Error.Message

	slog.Warn("429 response", "code", code, "message", msg)

	switch code {
	case "usage_limit_reached":
		// Hard usage cap — user's 5-hour/monthly budget exhausted
		ule := &UsageLimitError{
			ErrorType: code,
			Message:   msg,
			PlanType:  errResp.PlanType,
		}
		if errResp.ResetAt > 0 {
			ule.ResetsAt = time.Unix(errResp.ResetAt, 0)
		}
		return ule

	case "insufficient_quota":
		return &UsageLimitError{
			ErrorType: code,
			Message:   "API key has no remaining credits. Add credits at https://platform.openai.com/account/billing",
		}

	case "usage_not_included":
		return &UsageLimitError{
			ErrorType: code,
			Message:   "This feature is not available on your plan. Upgrade at https://platform.openai.com",
		}

	default:
		// Temporary rate limit — retryable
		retryAfter := 5 * time.Second
		// Try to extract delay from message (e.g. "try again in 28ms")
		if d := parseRetryDelay(msg); d > 0 {
			retryAfter = d
		}
		return &RateLimitError{
			StatusCode: status,
			RetryAfter: retryAfter,
			Body:       string(body),
		}
	}
}

// parseRetryDelay extracts a duration from messages like "try again in 28ms" or "try again in 1.5 seconds".
func parseRetryDelay(msg string) time.Duration {
	msg = strings.ToLower(msg)
	idx := strings.Index(msg, "try again in")
	if idx < 0 {
		return 0
	}
	after := strings.TrimSpace(msg[idx+len("try again in"):])

	var val float64
	var unit string
	if _, err := fmt.Sscanf(after, "%f%s", &val, &unit); err == nil {
		unit = strings.TrimSuffix(strings.TrimSpace(unit), ".")
		switch {
		case strings.HasPrefix(unit, "ms"):
			return time.Duration(val * float64(time.Millisecond))
		case strings.HasPrefix(unit, "s"):
			return time.Duration(val * float64(time.Second))
		case strings.HasPrefix(unit, "m"):
			return time.Duration(val * float64(time.Minute))
		}
	}
	return 0
}
