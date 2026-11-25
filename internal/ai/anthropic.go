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
	"sync/atomic"
	"time"
)

type AnthropicClient struct {
	cfg        Config
	baseURL    string
	apiKey     string
	httpClient *http.Client
	// noAutoCache is set once an endpoint rejects top-level automatic caching
	// (some proxies and partner platforms); the system-prompt breakpoint stays on.
	noAutoCache atomic.Bool
}

func NewAnthropicClient(baseURL, apiKey string, cfg Config) *AnthropicClient {
	return &AnthropicClient{cfg: cfg, baseURL: baseURL, apiKey: apiKey, httpClient: &http.Client{}}
}

func (c *AnthropicClient) SupportsNativeTools() bool { return true }

// --- Wire types ---

type anthRequest struct {
	Model        string            `json:"model"`
	MaxTokens    int               `json:"max_tokens"`
	System       []anthSystemBlock `json:"system,omitempty"`
	Messages     []anthMsg         `json:"messages"`
	Tools        []anthTool        `json:"tools,omitempty"`
	Thinking     *anthThinking     `json:"thinking,omitempty"`
	OutputConfig *anthOutputConfig `json:"output_config,omitempty"`
	// CacheControl enables automatic caching: the API puts a breakpoint on the
	// last cacheable block and moves it forward as the conversation grows.
	CacheControl *anthCacheControl `json:"cache_control,omitempty"`
}

type anthCacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type anthSystemBlock struct {
	Type         string            `json:"type"` // "text"
	Text         string            `json:"text"`
	CacheControl *anthCacheControl `json:"cache_control,omitempty"`
}

// systemBlocks returns the system prompt with a cache breakpoint on it.
// Tools render before the system prompt, so this one breakpoint caches the
// tool definitions and the system prompt together: the stable part shared by
// every turn of a run and by runs of the same bot.
func systemBlocks(prompt string) []anthSystemBlock {
	if prompt == "" {
		return nil
	}
	return []anthSystemBlock{{Type: "text", Text: prompt, CacheControl: &anthCacheControl{Type: "ephemeral"}}}
}

type anthThinking struct {
	Type         string `json:"type"`                    // "adaptive" (current models) or "enabled" (legacy)
	BudgetTokens int    `json:"budget_tokens,omitempty"` // legacy models only
}

type anthOutputConfig struct {
	Effort string `json:"effort,omitempty"`
}

type anthMsg struct {
	Role    string `json:"role"`
	Content any    `json:"content"` // string or []anthContentBlock
}

type anthContentBlock struct {
	Type      string  `json:"type"`                  // text, tool_use, tool_result, thinking, redacted_thinking
	Text      string  `json:"text,omitempty"`        // type=text
	ID        string  `json:"id,omitempty"`          // type=tool_use
	Name      string  `json:"name,omitempty"`        // type=tool_use
	Input     any     `json:"input,omitempty"`       // type=tool_use (always a non-nil map so it is never omitted)
	Thinking  *string `json:"thinking,omitempty"`    // type=thinking (may be empty string, must be sent)
	Signature string  `json:"signature,omitempty"`   // type=thinking
	Data      string  `json:"data,omitempty"`        // type=redacted_thinking
	ToolUseID string  `json:"tool_use_id,omitempty"` // type=tool_result
	Content   string  `json:"content,omitempty"`     // type=tool_result (can also be array, but we use string)
	IsError   bool    `json:"is_error,omitempty"`    // type=tool_result
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
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

type anthRespBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	Thinking  string         `json:"thinking,omitempty"`
	Signature string         `json:"signature,omitempty"`
	Data      string         `json:"data,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
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
		System:    systemBlocks(prompt),
		Messages:  aMsgs,
		Tools:     aTools,
	}

	c.applyThinking(&reqBody, model)

	hr, err := c.send(ctx, &reqBody)
	if err != nil {
		return nil, err
	}
	if hr.status == http.StatusTooManyRequests || hr.status == 529 {
		return nil, parse429Response(hr.status, hr.body)
	}
	if hr.status != http.StatusOK {
		return nil, fmt.Errorf("anthropic API error (HTTP %d): %s", hr.status, string(hr.body))
	}

	var resp anthResponse
	if err := json.Unmarshal(hr.body, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	result := &ChatResponse{
		StopReason: resp.StopReason,
		RateLimit:  parseAnthropicRateLimitHeaders(hr.headers),
	}
	if resp.Usage != nil {
		result.Usage = &TokenUsage{
			PromptTokens:     resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			CacheReadTokens:  resp.Usage.CacheReadInputTokens,
			CacheWriteTokens: resp.Usage.CacheCreationInputTokens,
		}
		slog.Info("Anthropic tool response", "input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens,
			"cache_read_tokens", resp.Usage.CacheReadInputTokens, "cache_write_tokens", resp.Usage.CacheCreationInputTokens)
		reportUsage(ctx, model, *result.Usage)
	}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			result.Content = append(result.Content, ContentBlock{Type: "text", Text: block.Text})
		case "thinking", "redacted_thinking":
			result.Content = append(result.Content, ContentBlock{
				Type: "thinking",
				Thinking: &ThinkingBlock{
					Type:      block.Type,
					Thinking:  block.Thinking,
					Signature: block.Signature,
					Data:      block.Data,
				},
			})
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
		// Assistant with tool calls. Thinking blocks go first, unchanged.
		var blocks []anthContentBlock
		for _, tb := range m.Thinking {
			b := anthContentBlock{Type: tb.Type, Signature: tb.Signature, Data: tb.Data}
			if tb.Type == "thinking" {
				t := tb.Thinking
				b.Thinking = &t
			}
			blocks = append(blocks, b)
		}
		if m.Content != "" {
			blocks = append(blocks, anthContentBlock{Type: "text", Text: m.Content})
		}
		for _, tc := range m.ToolCalls {
			var input any = tc.Input
			if tc.Input == nil {
				input = map[string]any{}
			}
			blocks = append(blocks, anthContentBlock{
				Type:  "tool_use",
				ID:    tc.ID,
				Name:  tc.Name,
				Input: input,
			})
		}
		return []anthMsg{{Role: "assistant", Content: blocks}}

	default:
		return []anthMsg{{Role: m.Role, Content: m.Content}}
	}
}

func (c *AnthropicClient) doHTTPFull(ctx context.Context, body []byte) (*httpResult, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return &httpResult{body: respBody, status: resp.StatusCode, headers: resp.Header}, nil
}

// send marshals and posts a request with automatic caching enabled. If the
// endpoint rejects the top-level cache_control field, the request is retried
// once without it and automatic caching stays off for this client.
func (c *AnthropicClient) send(ctx context.Context, req *anthRequest) (*httpResult, error) {
	if !c.noAutoCache.Load() {
		req.CacheControl = &anthCacheControl{Type: "ephemeral"}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	hr, err := c.doHTTPFull(ctx, body)
	if err != nil || req.CacheControl == nil || !rejectsAutoCache(hr) {
		return hr, err
	}
	slog.Warn("Endpoint rejects automatic prompt caching; continuing with the system-prompt breakpoint only",
		"base_url", c.baseURL)
	c.noAutoCache.Store(true)
	req.CacheControl = nil
	if body, err = json.Marshal(req); err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}
	return c.doHTTPFull(ctx, body)
}

// rejectsAutoCache reports whether a 400 is about the top-level cache_control
// field (not about a marker inside system or messages).
func rejectsAutoCache(hr *httpResult) bool {
	if hr.status != http.StatusBadRequest {
		return false
	}
	b := string(hr.body)
	return strings.Contains(b, "cache_control") && !strings.Contains(b, "messages.") && !strings.Contains(b, "system.")
}

// parseAnthropicRateLimitHeaders extracts usage from Anthropic-specific headers.
func parseAnthropicRateLimitHeaders(headers http.Header) *RateLimitSnapshot {
	// Anthropic uses utilization as 0-1 fraction
	// Check 5-hour first (more relevant for session limits)
	util5h := headers.Get("anthropic-ratelimit-unified-5h-utilization")
	util7d := headers.Get("anthropic-ratelimit-unified-7d-utilization")

	var used float64
	var resetStr string

	if util5h != "" {
		fmt.Sscanf(util5h, "%f", &used)
		used *= 100 // convert 0-1 to 0-100 percent
		resetStr = headers.Get("anthropic-ratelimit-unified-5h-reset")
	} else if util7d != "" {
		fmt.Sscanf(util7d, "%f", &used)
		used *= 100
		resetStr = headers.Get("anthropic-ratelimit-unified-7d-reset")
	}

	if used == 0 {
		return nil
	}

	snap := &RateLimitSnapshot{
		UsedPercent: used,
		LimitName:   headers.Get("anthropic-ratelimit-unified-representative-claim"),
	}

	if resetStr != "" {
		var resetUnix int64
		fmt.Sscanf(resetStr, "%d", &resetUnix)
		if resetUnix > 0 {
			snap.ResetsAt = time.Unix(resetUnix, 0)
		}
	}

	// Also check the status header for early detection
	status := headers.Get("anthropic-ratelimit-unified-status")
	if status == "allowed_warning" && used < 75 {
		// Server says we're approaching limits even though utilization looks ok
		// Bump to 75% so our warning logic triggers
		snap.UsedPercent = 75
	}

	return snap
}

func (c *AnthropicClient) doSimpleRequest(ctx context.Context, model string, maxTokens int, systemPrompt string, messages []anthMsg, logContext string) (string, error) {
	reqBody := anthRequest{Model: model, MaxTokens: maxTokens, System: systemBlocks(systemPrompt), Messages: messages}
	c.applyThinking(&reqBody, model)
	hr, err := c.send(ctx, &reqBody)
	if err != nil {
		return "", err
	}
	respBody, status := hr.body, hr.status
	if status == http.StatusTooManyRequests || status == 529 {
		return "", parse429Response(status, respBody)
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
	// Usage is reported before the empty-text check: an empty answer still costs tokens.
	if resp.Usage != nil {
		slog.Info("Anthropic response", "context", logContext,
			"input_tokens", resp.Usage.InputTokens, "output_tokens", resp.Usage.OutputTokens,
			"cache_read_tokens", resp.Usage.CacheReadInputTokens, "cache_write_tokens", resp.Usage.CacheCreationInputTokens)
		reportUsage(ctx, model, TokenUsage{
			PromptTokens:     resp.Usage.InputTokens + resp.Usage.CacheReadInputTokens + resp.Usage.CacheCreationInputTokens,
			CompletionTokens: resp.Usage.OutputTokens,
			CacheReadTokens:  resp.Usage.CacheReadInputTokens,
			CacheWriteTokens: resp.Usage.CacheCreationInputTokens,
		})
	}

	if text == "" {
		return "Unable to generate " + logContext + " - empty response from AI.", nil
	}

	return text, nil
}

// applyThinking sets the thinking/effort configuration for the request.
//
// Current Claude models (4.6 and later, including the 5.x family) use adaptive
// thinking and reject the legacy {type:"enabled", budget_tokens:N} form with a
// 400. On those models "Extended Thinking" maps to adaptive thinking at high
// effort; otherwise the model's defaults apply. Legacy models (4.5 and older)
// keep the budget_tokens form.
func (c *AnthropicClient) applyThinking(req *anthRequest, model string) {
	if !c.cfg.ExtendedThinking {
		return
	}
	if isLegacyThinkingModel(model) {
		budget := c.cfg.ThinkingBudget
		if budget < 1024 {
			return
		}
		if budget >= req.MaxTokens {
			// budget_tokens must be < max_tokens; give the answer room too.
			req.MaxTokens = budget + 4096
		}
		req.Thinking = &anthThinking{Type: "enabled", BudgetTokens: budget}
		slog.Debug("Extended thinking enabled (budget)", "model", model, "budget_tokens", budget)
		return
	}
	req.Thinking = &anthThinking{Type: "adaptive"}
	req.OutputConfig = &anthOutputConfig{Effort: "high"}
	slog.Debug("Extended thinking enabled (adaptive)", "model", model, "effort", "high")
}

// isLegacyThinkingModel reports whether the model predates adaptive thinking
// and still requires budget_tokens (Claude 3.x and Claude 4.0/4.1/4.5).
func isLegacyThinkingModel(model string) bool {
	m := strings.ToLower(model)
	if strings.HasPrefix(m, "claude-3") {
		return true
	}
	for _, v := range []string{"-4-5", "-4-1", "-4-0", "-4-2025"} {
		if strings.Contains(m, v) {
			return true
		}
	}
	return false
}
