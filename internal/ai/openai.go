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

// OpenAIClient implements Client for OpenAI-compatible APIs.
type OpenAIClient struct {
	cfg        Config
	baseURL    string
	apiKey     string
	httpClient *http.Client
	// OAuth fields (used when auth_method is "oauth")
	accessToken string
	accountID   string
}

func NewOpenAIClient(baseURL, apiKey string, cfg Config) *OpenAIClient {
	return &OpenAIClient{
		cfg:        cfg,
		baseURL:    baseURL,
		apiKey:     apiKey,
		httpClient: &http.Client{},
	}
}

// NewOpenAIClientWithOAuth creates an OpenAI client using OAuth tokens.
func NewOpenAIClientWithOAuth(baseURL, accessToken, accountID string, cfg Config) *OpenAIClient {
	return &OpenAIClient{
		cfg:         cfg,
		baseURL:     baseURL,
		accessToken: accessToken,
		accountID:   accountID,
		httpClient:  &http.Client{},
	}
}

type openaiRequest struct {
	Model    string           `json:"model"`
	Messages []openaiMessage  `json:"messages"`
	MaxTokens int             `json:"max_completion_tokens"`
}

type openaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func (c *OpenAIClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	return reviewDiffCommon(c.cfg, req, func(systemPrompt, model string, maxTokens int, userMsg string) (string, error) {
		messages := []openaiMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMsg},
		}
		return c.doRequest(ctx, model, maxTokens, messages, "review")
	})
}

func (c *OpenAIClient) Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	return chatCommon(c.cfg, history, userMessage, opts, func(systemPrompt, model string, maxTokens int, messages []Message) (string, error) {
		oaiMsgs := make([]openaiMessage, 0, len(messages)+1)
		oaiMsgs = append(oaiMsgs, openaiMessage{Role: "system", Content: systemPrompt})
		for _, m := range messages {
			oaiMsgs = append(oaiMsgs, openaiMessage{Role: m.Role, Content: m.Content})
		}
		return c.doRequest(ctx, model, maxTokens, oaiMsgs, "chat")
	})
}

func (c *OpenAIClient) doRequest(ctx context.Context, model string, maxTokens int, messages []openaiMessage, logContext string) (string, error) {
	reqBody := openaiRequest{
		Model:    model,
		Messages: messages,
		MaxTokens: maxTokens,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	// Use OAuth token or API key
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
		return "", fmt.Errorf("openai request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var oaiResp openaiResponse
	if err := json.Unmarshal(respBody, &oaiResp); err != nil {
		return "", fmt.Errorf("parse openai response: %w", err)
	}

	if len(oaiResp.Choices) == 0 || oaiResp.Choices[0].Message.Content == "" {
		return "Unable to generate " + logContext + " - empty response from AI.", nil
	}

	if oaiResp.Usage != nil {
		slog.Info("OpenAI response", "context", logContext,
			"prompt_tokens", oaiResp.Usage.PromptTokens,
			"completion_tokens", oaiResp.Usage.CompletionTokens)
	}

	return oaiResp.Choices[0].Message.Content, nil
}
