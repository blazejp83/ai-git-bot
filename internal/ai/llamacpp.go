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

// LlamaCppClient implements Client for llama.cpp server's /completion endpoint.
type LlamaCppClient struct {
	cfg        Config
	baseURL    string
	httpClient *http.Client
}

func NewLlamaCppClient(baseURL string, cfg Config) *LlamaCppClient {
	return &LlamaCppClient{
		cfg:        cfg,
		baseURL:    baseURL,
		httpClient: &http.Client{},
	}
}

// GBNF grammar for structured JSON output (used by agent feature).
const agentJSONGrammar = `root ::= "{" ws members ws "}" ws
members ::= pair ("," ws pair)*
pair ::= string ws ":" ws value
value ::= string | number | object | array | "true" | "false" | "null"
object ::= "{" ws (members ws)? "}"
array ::= "[" ws (value ("," ws value)*)? ws "]"
string ::= "\"" ([^"\\x00-\x1f] | "\\" ["\/bfnrt] | "\\u" [0-9a-fA-F]{4})* "\""
number ::= "-"? ([0-9] | [1-9] [0-9]*) ("." [0-9]+)? ([eE] [-+]? [0-9]+)?
ws ::= [ \t\n\r]*`

var stopSequences = []string{
	"<|im_start|>", "<|im_end|>", "<|end|>", "<|eot_id|>", "<|endoftext|>",
}

type llamaCppRequest struct {
	Prompt           string   `json:"prompt"`
	NPredict         int      `json:"n_predict"`
	Stream           bool     `json:"stream"`
	Stop             []string `json:"stop,omitempty"`
	Temperature      float64  `json:"temperature"`
	TopP             float64  `json:"top_p"`
	TopK             int      `json:"top_k"`
	RepeatPenalty    float64  `json:"repeat_penalty"`
	FrequencyPenalty float64  `json:"frequency_penalty"`
	PresencePenalty  float64  `json:"presence_penalty"`
	CachePrompt      bool     `json:"cache_prompt"`
	Grammar          string   `json:"grammar,omitempty"`
}

type llamaCppResponse struct {
	Content         string `json:"content"`
	TokensEvaluated *int   `json:"tokens_evaluated"`
	TokensPredicted *int   `json:"tokens_predicted"`
	StoppedLimit    *bool  `json:"stopped_limit"`
}

func (c *LlamaCppClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	return reviewDiffCommon(c.cfg, req, func(systemPrompt, model string, maxTokens int, userMsg string) (string, error) {
		prompt := buildChatMLPrompt(systemPrompt, userMsg)
		grammar := ""
		if shouldUseJsonGrammar(systemPrompt) {
			grammar = agentJSONGrammar
		}
		return c.doRequest(ctx, prompt, maxTokens, grammar, "review")
	})
}

func (c *LlamaCppClient) Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	return chatCommon(c.cfg, history, userMessage, opts, func(systemPrompt, model string, maxTokens int, messages []Message) (string, error) {
		prompt := buildChatMLPromptFromHistory(systemPrompt, messages)
		grammar := ""
		if shouldUseJsonGrammar(systemPrompt) {
			grammar = agentJSONGrammar
		}
		return c.doRequest(ctx, prompt, maxTokens, grammar, "chat")
	})
}

// buildChatMLPrompt creates a ChatML-format prompt for a single user message.
func buildChatMLPrompt(systemPrompt, userMessage string) string {
	var sb strings.Builder
	sb.WriteString("<|im_start|>system\n")
	sb.WriteString(systemPrompt)
	sb.WriteString("<|im_end|>\n")
	sb.WriteString("<|im_start|>user\n")
	sb.WriteString(userMessage)
	sb.WriteString("<|im_end|>\n")
	sb.WriteString("<|im_start|>assistant\n")
	return sb.String()
}

// buildChatMLPromptFromHistory creates a ChatML-format prompt from conversation history.
func buildChatMLPromptFromHistory(systemPrompt string, messages []Message) string {
	var sb strings.Builder
	sb.WriteString("<|im_start|>system\n")
	sb.WriteString(systemPrompt)
	sb.WriteString("<|im_end|>\n")
	for _, msg := range messages {
		sb.WriteString("<|im_start|>")
		sb.WriteString(msg.Role)
		sb.WriteString("\n")
		sb.WriteString(msg.Content)
		sb.WriteString("<|im_end|>\n")
	}
	sb.WriteString("<|im_start|>assistant\n")
	return sb.String()
}

func shouldUseJsonGrammar(systemPrompt string) bool {
	lower := strings.ToLower(systemPrompt)
	return strings.Contains(lower, "respond with a json") ||
		strings.Contains(lower, "output json") ||
		(strings.Contains(lower, "output format") && strings.Contains(lower, "json")) ||
		strings.Contains(lower, "\"filechanges\"") ||
		strings.Contains(lower, "\"runtool\"")
}

func (c *LlamaCppClient) doRequest(ctx context.Context, prompt string, maxTokens int, grammar, logContext string) (string, error) {
	reqBody := llamaCppRequest{
		Prompt:           prompt,
		NPredict:         maxTokens,
		Stream:           false,
		Stop:             stopSequences,
		Temperature:      0.7,
		TopP:             0.9,
		TopK:             40,
		RepeatPenalty:    1.1,
		FrequencyPenalty: 0.0,
		PresencePenalty:  0.0,
		CachePrompt:     true,
	}
	if grammar != "" {
		reqBody.Grammar = grammar
		slog.Info("llama.cpp request: GBNF grammar enabled", "context", logContext)
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+"/completion", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("llamacpp request: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("llamacpp API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var lcResp llamaCppResponse
	if err := json.Unmarshal(respBody, &lcResp); err != nil {
		return "", fmt.Errorf("parse llamacpp response: %w", err)
	}

	if lcResp.Content == "" {
		return "Unable to generate " + logContext + " - empty response from AI.", nil
	}

	if lcResp.TokensEvaluated != nil && lcResp.TokensPredicted != nil {
		slog.Info("llama.cpp response", "context", logContext,
			"prompt_tokens", *lcResp.TokensEvaluated,
			"generated_tokens", *lcResp.TokensPredicted)
	}
	if lcResp.StoppedLimit != nil && *lcResp.StoppedLimit {
		slog.Warn("llama.cpp response truncated due to max token limit", "context", logContext)
	}

	return lcResp.Content, nil
}
