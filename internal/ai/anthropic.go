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

// AnthropicClient implements Client for the Anthropic Messages API.
type AnthropicClient struct {
	cfg        Config
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewAnthropicClient(baseURL, apiKey string, cfg Config) *AnthropicClient {
	return &AnthropicClient{
		cfg:        cfg,
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage *struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
}

func (c *AnthropicClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	return reviewDiffCommon(c.cfg, req, func(systemPrompt, model string, maxTokens int, userMsg string) (string, error) {
		messages := []anthropicMessage{{Role: "user", Content: userMsg}}
		return c.doRequest(ctx, model, maxTokens, systemPrompt, messages, "review")
	})
}

func (c *AnthropicClient) Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	return chatCommon(c.cfg, history, userMessage, opts, func(systemPrompt, model string, maxTokens int, messages []Message) (string, error) {
		aMsgs := make([]anthropicMessage, len(messages))
		for i, m := range messages {
			aMsgs[i] = anthropicMessage{Role: m.Role, Content: m.Content}
		}
		return c.doRequest(ctx, model, maxTokens, systemPrompt, aMsgs, "chat")
	})
}

func (c *AnthropicClient) doRequest(ctx context.Context, model string, maxTokens int, systemPrompt string, messages []anthropicMessage, logContext string) (string, error) {
	reqBody := anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    systemPrompt,
		Messages:  messages,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("anthropic request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("anthropic API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var aResp anthropicResponse
	if err := json.Unmarshal(respBody, &aResp); err != nil {
		return "", fmt.Errorf("parse anthropic response: %w", err)
	}

	var text string
	for _, block := range aResp.Content {
		if block.Type == "text" {
			text += block.Text
		}
	}
	if text == "" {
		return "Unable to generate " + logContext + " - empty response from AI.", nil
	}

	if aResp.Usage != nil {
		slog.Info("Anthropic response", "context", logContext,
			"input_tokens", aResp.Usage.InputTokens,
			"output_tokens", aResp.Usage.OutputTokens)
	}

	return text, nil
}
