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

type LlamaCppClient struct {
	cfg        Config
	baseURL    string
	httpClient *http.Client
}

func NewLlamaCppClient(baseURL string, cfg Config) *LlamaCppClient {
	return &LlamaCppClient{cfg: cfg, baseURL: baseURL, httpClient: &http.Client{}}
}

// llama.cpp doesn't support native tool calling — we use JSON shim
func (c *LlamaCppClient) SupportsNativeTools() bool { return false }

// GBNF grammar for structured JSON output
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

type lcppRequest struct {
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

type lcppResponse struct {
	Content         string `json:"content"`
	TokensEvaluated *int   `json:"tokens_evaluated"`
	TokensPredicted *int   `json:"tokens_predicted"`
	StoppedLimit    *bool  `json:"stopped_limit"`
}

// --- Existing methods ---

func (c *LlamaCppClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	return reviewDiffCommon(c.cfg, req, func(systemPrompt, model string, maxTokens int, userMsg string) (string, error) {
		prompt := buildChatMLPrompt(systemPrompt, userMsg)
		return c.doRequest(ctx, prompt, maxTokens, "", "review")
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

// --- ChatWithTools (JSON shim) ---

func (c *LlamaCppClient) ChatWithTools(ctx context.Context, messages []ConversationMessage, tools []ToolDef, opts ChatOpts) (*ChatResponse, error) {
	model := resolveModel(opts.ModelOverride, c.cfg.Model)
	_ = model // llama.cpp doesn't use model name in request
	prompt := resolvePrompt(opts.SystemPrompt)
	maxTokens := c.cfg.MaxTokens
	if opts.MaxTokensOverride > 0 {
		maxTokens = opts.MaxTokensOverride
	}

	// Inject tool descriptions into system prompt
	toolPrompt := buildToolSystemPrompt(prompt, tools)

	// Build ChatML prompt from conversation
	var convMsgs []Message
	for _, m := range messages {
		if len(m.ToolResults) > 0 {
			// Format tool results as user message
			var sb strings.Builder
			for _, tr := range m.ToolResults {
				fmt.Fprintf(&sb, "Tool result for %s:\n%s\n", tr.ToolName, tr.Content)
			}
			convMsgs = append(convMsgs, Message{Role: "user", Content: sb.String()})
		} else if len(m.ToolCalls) > 0 {
			// Format tool calls as assistant message
			raw, _ := json.Marshal(map[string]any{"tool": m.ToolCalls[0].Name, "input": m.ToolCalls[0].Input})
			convMsgs = append(convMsgs, Message{Role: "assistant", Content: string(raw)})
		} else {
			convMsgs = append(convMsgs, Message{Role: m.Role, Content: m.Content})
		}
	}

	chatPrompt := buildChatMLPromptFromHistory(toolPrompt, convMsgs)
	text, err := c.doRequest(ctx, chatPrompt, maxTokens, agentJSONGrammar, "tools")
	if err != nil {
		return nil, err
	}

	// Parse JSON response into tool call or text
	return parseShimResponse(text), nil
}

func buildToolSystemPrompt(basePrompt string, tools []ToolDef) string {
	var sb strings.Builder
	sb.WriteString(basePrompt)
	sb.WriteString("\n\nYou have the following tools available. To use a tool, respond with ONLY a JSON object:\n")
	sb.WriteString(`{"tool": "<tool_name>", "input": {<arguments>}}`)
	sb.WriteString("\n\nAvailable tools:\n")
	for _, t := range tools {
		sb.WriteString("- ")
		sb.WriteString(t.Name)
		sb.WriteString(": ")
		sb.WriteString(t.Description)
		sb.WriteString("\n")
	}
	sb.WriteString("\nWhen you have completed the task, call the \"done\" tool with your final result.\n")
	sb.WriteString("If you want to say something without calling a tool, use: {\"tool\": \"done\", \"input\": {\"result\": \"your message\"}}\n")
	return sb.String()
}

func parseShimResponse(text string) *ChatResponse {
	text = strings.TrimSpace(text)

	// Try to parse as JSON tool call
	var toolJSON struct {
		Tool  string         `json:"tool"`
		Input map[string]any `json:"input"`
	}
	if err := json.Unmarshal([]byte(text), &toolJSON); err == nil && toolJSON.Tool != "" {
		raw, _ := json.Marshal(toolJSON.Input)
		return &ChatResponse{
			StopReason: "tool_use",
			Content: []ContentBlock{{
				Type: "tool_use",
				ToolCall: &ToolCall{
					ID:       fmt.Sprintf("shim_%d", len(text)),
					Name:     toolJSON.Tool,
					Input:    toolJSON.Input,
					RawInput: string(raw),
				},
			}},
		}
	}

	// Not a tool call — return as text
	return &ChatResponse{
		StopReason: "end_turn",
		Content:    []ContentBlock{{Type: "text", Text: text}},
	}
}

// --- Helpers ---

func buildChatMLPrompt(systemPrompt, userMessage string) string {
	var sb strings.Builder
	sb.WriteString("<|im_start|>system\n")
	sb.WriteString(systemPrompt)
	sb.WriteString("<|im_end|>\n<|im_start|>user\n")
	sb.WriteString(userMessage)
	sb.WriteString("<|im_end|>\n<|im_start|>assistant\n")
	return sb.String()
}

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
	reqBody := lcppRequest{
		Prompt: prompt, NPredict: maxTokens, Stream: false,
		Stop: stopSequences, Temperature: 0.7, TopP: 0.9, TopK: 40,
		RepeatPenalty: 1.1, CachePrompt: true,
	}
	if grammar != "" {
		reqBody.Grammar = grammar
	}

	body, _ := json.Marshal(reqBody)
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

	var lcResp lcppResponse
	if err := json.Unmarshal(respBody, &lcResp); err != nil {
		return "", fmt.Errorf("parse response: %w", err)
	}

	if lcResp.Content == "" {
		return "Unable to generate " + logContext + " - empty response from AI.", nil
	}

	if lcResp.TokensEvaluated != nil && lcResp.TokensPredicted != nil {
		slog.Info("llama.cpp response", "context", logContext,
			"prompt_tokens", *lcResp.TokensEvaluated, "generated_tokens", *lcResp.TokensPredicted)
	}

	return lcResp.Content, nil
}
