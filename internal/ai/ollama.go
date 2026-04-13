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
)

// OllamaClient implements Client for Ollama local LLM inference.
type OllamaClient struct {
	cfg        Config
	baseURL    string
	httpClient *http.Client
}

func NewOllamaClient(baseURL string, cfg Config) *OllamaClient {
	return &OllamaClient{
		cfg:        cfg,
		baseURL:    baseURL,
		httpClient: &http.Client{},
	}
}

type ollamaRequest struct {
	Model    string           `json:"model"`
	Messages []ollamaMessage  `json:"messages"`
	Stream   bool             `json:"stream"`
	Format   string           `json:"format,omitempty"`
	Options  *ollamaOptions   `json:"options,omitempty"`
}

type ollamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaOptions struct {
	NumPredict int `json:"num_predict,omitempty"`
}

type ollamaResponse struct {
	Message *struct {
		Content string `json:"content"`
	} `json:"message"`
	PromptEvalCount *int `json:"prompt_eval_count"`
	EvalCount       *int `json:"eval_count"`
}

func (c *OllamaClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	return reviewDiffCommon(c.cfg, req, func(systemPrompt, model string, maxTokens int, userMsg string) (string, error) {
		messages := []ollamaMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMsg},
		}
		return c.doRequest(ctx, model, maxTokens, messages, systemPrompt, "review")
	})
}

func (c *OllamaClient) Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	return chatCommon(c.cfg, history, userMessage, opts, func(systemPrompt, model string, maxTokens int, messages []Message) (string, error) {
		oMsgs := make([]ollamaMessage, 0, len(messages)+1)
		oMsgs = append(oMsgs, ollamaMessage{Role: "system", Content: systemPrompt})
		for _, m := range messages {
			oMsgs = append(oMsgs, ollamaMessage{Role: m.Role, Content: m.Content})
		}
		return c.doRequest(ctx, model, maxTokens, oMsgs, systemPrompt, "chat")
	})
}

func shouldUseJsonMode(systemPrompt string) bool {
	lower := strings.ToLower(systemPrompt)
	return strings.Contains(lower, "respond with a json") ||
		strings.Contains(lower, "output json") ||
		(strings.Contains(lower, "output format") && strings.Contains(lower, "json")) ||
		strings.Contains(lower, "```json")
}

func (c *OllamaClient) doRequest(ctx context.Context, model string, maxTokens int, messages []ollamaMessage, systemPrompt, logContext string) (string, error) {
	reqBody := ollamaRequest{
		Model:    model,
		Messages: messages,
		Stream:   false,
		Options:  &ollamaOptions{NumPredict: maxTokens},
	}
	if shouldUseJsonMode(systemPrompt) {
		reqBody.Format = "json"
		slog.Info("Ollama request: JSON mode enabled", "context", logContext)
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

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
		return "", fmt.Errorf("parse ollama response: %w", err)
	}

	if oResp.Message == nil || oResp.Message.Content == "" {
		return "Unable to generate " + logContext + " - empty response from AI.", nil
	}

	if oResp.PromptEvalCount != nil && oResp.EvalCount != nil {
		slog.Info("Ollama response", "context", logContext,
			"prompt_tokens", *oResp.PromptEvalCount,
			"eval_tokens", *oResp.EvalCount)
	}

	return oResp.Message.Content, nil
}
