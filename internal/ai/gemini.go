package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// GeminiClient implements Client using the Gemini CLI (non-interactive instrumentation).
// Auth is handled by the CLI itself — no API keys or OAuth needed.
type GeminiClient struct {
	cfg    Config
	apiKey string // optional; passed as GEMINI_API_KEY env var to the CLI
}

func NewGeminiClient(cfg Config, apiKey string) *GeminiClient {
	return &GeminiClient{cfg: cfg, apiKey: apiKey}
}

func (c *GeminiClient) SupportsNativeTools() bool { return false }

func (c *GeminiClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	return reviewDiffCommon(c.cfg, req, func(systemPrompt, model string, maxTokens int, userMsg string) (string, error) {
		slog.Info("Requesting code review via Gemini CLI", "model", model)
		return invokeGemini(ctx, model, systemPrompt, userMsg, "", c.apiKey, defaultCLITimeout)
	})
}

func (c *GeminiClient) Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	return chatCommon(c.cfg, history, userMessage, opts, func(systemPrompt, model string, maxTokens int, messages []Message) (string, error) {
		combined := flattenMessages(messages)
		slog.Info("Sending chat via Gemini CLI", "model", model, "history_size", len(messages))
		return invokeGemini(ctx, model, systemPrompt, combined, opts.WorkDir, c.apiKey, defaultCLITimeout)
	})
}

func (c *GeminiClient) ChatWithTools(ctx context.Context, messages []ConversationMessage, tools []ToolDef, opts ChatOpts) (*ChatResponse, error) {
	model := resolveModel(opts.ModelOverride, c.cfg.Model)
	prompt := resolvePrompt(opts.SystemPrompt)

	combined := flattenConversation(messages)

	slog.Info("ChatWithTools via Gemini CLI", "model", model, "messages", len(messages), "tools", len(tools))
	response, err := invokeGemini(ctx, model, prompt, combined, opts.WorkDir, c.apiKey, defaultCLITimeout)
	if err != nil {
		return nil, fmt.Errorf("gemini ChatWithTools: %w", err)
	}

	if response == "" {
		response = "Unable to generate response — empty output from Gemini CLI."
	}

	return &ChatResponse{
		StopReason: "end_turn",
		Content:    []ContentBlock{{Type: "text", Text: response}},
	}, nil
}

// --- CLI execution ---

// findGeminiBin locates the gemini binary on the system.
func findGeminiBin() (string, error) {
	if path, err := exec.LookPath("gemini"); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	fallbacks := []string{
		filepath.Join(home, ".local", "bin", "gemini"),
		"/usr/local/bin/gemini",
		"/usr/bin/gemini",
	}
	for _, path := range fallbacks {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("gemini binary not found — install Gemini CLI: https://github.com/google-gemini/gemini-cli")
}

// invokeGemini runs the gemini CLI non-interactively and returns the response text.
func invokeGemini(ctx context.Context, model, systemPrompt, userMessage, workDir, apiKey string, timeout time.Duration) (string, error) {
	bin, err := findGeminiBin()
	if err != nil {
		return "", err
	}

	// Resolve working directory
	dir := workDir
	if dir == "" {
		dir, err = os.MkdirTemp("", "gemini-work-")
		if err != nil {
			return "", fmt.Errorf("create temp dir: %w", err)
		}
		defer os.RemoveAll(dir)
	}

	// Write system prompt to GEMINI.md for gemini auto-discovery
	if systemPrompt != "" {
		geminiPath := filepath.Join(dir, "GEMINI.md")
		existed := fileExists(geminiPath)
		if err := os.WriteFile(geminiPath, []byte(systemPrompt), 0644); err != nil {
			slog.Warn("Failed to write GEMINI.md", "err", err)
		} else if !existed {
			defer os.Remove(geminiPath)
		}
	}

	args := []string{
		"--output-format", "json",
		"--yolo",
		"-m", model,
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	proc := exec.CommandContext(execCtx, bin, args...)
	proc.Stdin = strings.NewReader(userMessage)
	proc.Dir = dir
	proc.Env = buildCLIEnv(apiKey, "GEMINI_API_KEY")

	slog.Debug("Invoking gemini CLI", "model", model, "dir", dir, "msg_len", len(userMessage))

	output, err := proc.Output()
	if err != nil {
		stderr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		return "", fmt.Errorf("gemini CLI failed: %w — stderr: %s", err, truncateStr(stderr, 500))
	}

	raw := strings.TrimSpace(string(output))
	if raw == "" {
		return "", fmt.Errorf("gemini CLI returned empty output")
	}

	return parseGeminiJSON(raw)
}

// parseGeminiJSON parses the JSON output from `gemini --output-format json`.
func parseGeminiJSON(raw string) (string, error) {
	var data struct {
		Response  string `json:"response"`
		SessionID string `json:"session_id"`
		Error     *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		return "", fmt.Errorf("parse gemini JSON output: %w", err)
	}

	if data.Error != nil && data.Error.Message != "" {
		return "", fmt.Errorf("gemini error: %s", data.Error.Message)
	}

	return data.Response, nil
}
