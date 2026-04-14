package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type OllamaClient struct {
	cfg        Config
	baseURL    string
	httpClient *http.Client
}

func NewOllamaClient(baseURL string, cfg Config) *OllamaClient {
	return &OllamaClient{cfg: cfg, baseURL: baseURL, httpClient: &http.Client{}}
}

func (c *OllamaClient) SupportsNativeTools() bool { return true }

// --- Wire types ---

type ollamaRequest struct {
	Model    string        `json:"model"`
	Messages []ollamaMsg   `json:"messages"`
	Stream   bool          `json:"stream"`
	Format   string        `json:"format,omitempty"`
	Options  *ollamaOpts   `json:"options,omitempty"`
	Tools    []oaiTool     `json:"tools,omitempty"` // OpenAI-compatible format
}

type ollamaMsg struct {
	Role      string        `json:"role"`
	Content   string        `json:"content"`
	ToolCalls []oaiToolCall `json:"tool_calls,omitempty"`
}

type ollamaOpts struct {
	NumPredict int `json:"num_predict,omitempty"`
}

type ollamaResponse struct {
	Message *struct {
		Content   string        `json:"content"`
		ToolCalls []oaiToolCall `json:"tool_calls"`
	} `json:"message"`
	PromptEvalCount *int `json:"prompt_eval_count"`
	EvalCount       *int `json:"eval_count"`
}

// --- Existing methods ---

func (c *OllamaClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	return reviewDiffCommon(c.cfg, req, func(systemPrompt, model string, maxTokens int, userMsg string) (string, error) {
		messages := []ollamaMsg{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMsg},
		}
		return c.doSimpleRequest(ctx, model, maxTokens, messages, systemPrompt, "review")
	})
}

func (c *OllamaClient) Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	return chatCommon(c.cfg, history, userMessage, opts, func(systemPrompt, model string, maxTokens int, messages []Message) (string, error) {
		oMsgs := make([]ollamaMsg, 0, len(messages)+1)
		oMsgs = append(oMsgs, ollamaMsg{Role: "system", Content: systemPrompt})
		for _, m := range messages {
			oMsgs = append(oMsgs, ollamaMsg{Role: m.Role, Content: m.Content})
		}
		return c.doSimpleRequest(ctx, model, maxTokens, oMsgs, systemPrompt, "chat")
	})
}

// --- ChatWithTools ---

func (c *OllamaClient) ChatWithTools(ctx context.Context, messages []ConversationMessage, tools []ToolDef, opts ChatOpts) (*ChatResponse, error) {
	model := resolveModel(opts.ModelOverride, c.cfg.Model)
	prompt := resolvePrompt(opts.SystemPrompt)
	maxTokens := c.cfg.MaxTokens
	if opts.MaxTokensOverride > 0 {
		maxTokens = opts.MaxTokensOverride
	}

	oMsgs := []ollamaMsg{{Role: "system", Content: prompt}}
	for _, m := range messages {
		oMsgs = append(oMsgs, convToOllama(m)...)
	}

	var oTools []oaiTool
	for _, t := range tools {
		oTools = append(oTools, oaiTool{
			Type:     "function",
			Function: oaiFunction{Name: t.Name, Description: t.Description, Parameters: t.Parameters},
		})
	}

	reqBody := ollamaRequest{
		Model:    model,
		Messages: oMsgs,
		Stream:   false,
		Options:  &ollamaOpts{NumPredict: maxTokens},
		Tools:    oTools,
	}

	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, parse429Response(resp.StatusCode, respBody)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var oResp ollamaResponse
	if err := json.Unmarshal(respBody, &oResp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	result := &ChatResponse{StopReason: "end_turn"}
	if oResp.PromptEvalCount != nil && oResp.EvalCount != nil {
		result.Usage = &TokenUsage{PromptTokens: *oResp.PromptEvalCount, CompletionTokens: *oResp.EvalCount}
	}

	if oResp.Message != nil {
		if oResp.Message.Content != "" {
			result.Content = append(result.Content, ContentBlock{Type: "text", Text: oResp.Message.Content})
		}
		for _, tc := range oResp.Message.ToolCalls {
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
			result.StopReason = "tool_use"
		}
	}

	return result, nil
}

func convToOllama(m ConversationMessage) []ollamaMsg {
	switch {
	case len(m.ToolResults) > 0:
		var msgs []ollamaMsg
		for _, tr := range m.ToolResults {
			msgs = append(msgs, ollamaMsg{Role: "tool", Content: tr.Content})
		}
		return msgs
	case len(m.ToolCalls) > 0:
		var calls []oaiToolCall
		for _, tc := range m.ToolCalls {
			raw := tc.RawInput
			if raw == "" {
				b, _ := json.Marshal(tc.Input)
				raw = string(b)
			}
			calls = append(calls, oaiToolCall{
				ID: tc.ID, Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: tc.Name, Arguments: raw},
			})
		}
		return []ollamaMsg{{Role: "assistant", Content: m.Content, ToolCalls: calls}}
	default:
		return []ollamaMsg{{Role: m.Role, Content: m.Content}}
	}
}

func shouldUseJsonMode(systemPrompt string) bool {
	lower := strings.ToLower(systemPrompt)
	return strings.Contains(lower, "respond with a json") ||
		strings.Contains(lower, "output json") ||
		(strings.Contains(lower, "output format") && strings.Contains(lower, "json")) ||
		strings.Contains(lower, "```json")
}

func (c *OllamaClient) doSimpleRequest(ctx context.Context, model string, maxTokens int, messages []ollamaMsg, systemPrompt, logContext string) (string, error) {
	reqBody := ollamaRequest{
		Model: model, Messages: messages, Stream: false,
		Options: &ollamaOpts{NumPredict: maxTokens},
	}
	if shouldUseJsonMode(systemPrompt) {
		reqBody.Format = "json"
	}

	body, _ := json.Marshal(reqBody)
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("ollama request: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ollama API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var oResp ollamaResponse
	if err := json.Unmarshal(respBody, &oResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if oResp.Message == nil || oResp.Message.Content == "" {
		return "Unable to generate " + logContext + " - empty response from AI.", nil
	}
	return oResp.Message.Content, nil
}
