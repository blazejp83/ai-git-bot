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

const defaultCLITimeout = 600 * time.Second // 10 minutes

// CodexClient implements Client using the Codex CLI (non-interactive instrumentation).
// Auth is handled by the CLI itself — no API keys or OAuth needed.
type CodexClient struct {
	cfg    Config
	apiKey string // optional; passed as OPENAI_API_KEY env var to the CLI
}

func NewCodexClient(cfg Config, apiKey string) *CodexClient {
	return &CodexClient{cfg: cfg, apiKey: apiKey}
}

func (c *CodexClient) SupportsNativeTools() bool { return false }

func (c *CodexClient) ReviewDiff(ctx context.Context, req ReviewRequest) (string, error) {
	return reviewDiffCommon(c.cfg, req, func(systemPrompt, model string, maxTokens int, userMsg string) (string, error) {
		slog.Info("Requesting code review via Codex CLI", "model", model)
		return invokeCodex(ctx, model, systemPrompt, userMsg, "", c.apiKey, defaultCLITimeout)
	})
}

func (c *CodexClient) Chat(ctx context.Context, history []Message, userMessage string, opts ChatOpts) (string, error) {
	return chatCommon(c.cfg, history, userMessage, opts, func(systemPrompt, model string, maxTokens int, messages []Message) (string, error) {
		combined := flattenMessages(messages)
		slog.Info("Sending chat via Codex CLI", "model", model, "history_size", len(messages))
		return invokeCodex(ctx, model, systemPrompt, combined, opts.WorkDir, c.apiKey, defaultCLITimeout)
	})
}

func (c *CodexClient) ChatWithTools(ctx context.Context, messages []ConversationMessage, tools []ToolDef, opts ChatOpts) (*ChatResponse, error) {
	model := resolveModel(opts.ModelOverride, c.cfg.Model)
	prompt := resolvePrompt(opts.SystemPrompt)

	combined := flattenConversation(messages)

	slog.Info("ChatWithTools via Codex CLI", "model", model, "messages", len(messages), "tools", len(tools))
	response, err := invokeCodex(ctx, model, prompt, combined, opts.WorkDir, c.apiKey, defaultCLITimeout)
	if err != nil {
		return nil, fmt.Errorf("codex ChatWithTools: %w", err)
	}

	if response == "" {
		response = "Unable to generate response — empty output from Codex CLI."
	}

	return &ChatResponse{
		StopReason: "end_turn",
		Content:    []ContentBlock{{Type: "text", Text: response}},
	}, nil
}

// --- CLI execution ---

// findCodexBin locates the codex binary on the system.
func findCodexBin() (string, error) {
	if path, err := exec.LookPath("codex"); err == nil {
		return path, nil
	}
	home, _ := os.UserHomeDir()
	fallbacks := []string{
		filepath.Join(home, ".local", "bin", "codex"),
		"/usr/local/bin/codex",
		"/usr/bin/codex",
	}
	for _, path := range fallbacks {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("codex binary not found — install Codex CLI: https://github.com/openai/codex")
}

// invokeCodex runs `codex exec` non-interactively and returns the response text.
func invokeCodex(ctx context.Context, model, systemPrompt, userMessage, workDir, apiKey string, timeout time.Duration) (string, error) {
	bin, err := findCodexBin()
	if err != nil {
		return "", err
	}

	// Resolve working directory
	dir := workDir
	if dir == "" {
		dir, err = os.MkdirTemp("", "codex-work-")
		if err != nil {
			return "", fmt.Errorf("create temp dir: %w", err)
		}
		defer os.RemoveAll(dir)
	}

	// Write system prompt to AGENTS.md for codex auto-discovery
	if systemPrompt != "" {
		agentsPath := filepath.Join(dir, "AGENTS.md")
		existed := fileExists(agentsPath)
		if err := os.WriteFile(agentsPath, []byte(systemPrompt), 0644); err != nil {
			slog.Warn("Failed to write AGENTS.md", "err", err)
		} else if !existed {
			defer os.Remove(agentsPath)
		}
	}

	args := []string{
		"exec",
		"--json",
		"--full-auto",
		"--skip-git-repo-check",
		"-m", model,
		"-", // read prompt from stdin
	}

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	proc := exec.CommandContext(execCtx, bin, args...)
	proc.Stdin = strings.NewReader(userMessage)
	proc.Dir = dir
	proc.Env = buildCLIEnv(apiKey, "OPENAI_API_KEY")

	slog.Debug("Invoking codex CLI", "model", model, "dir", dir, "msg_len", len(userMessage))

	output, err := proc.Output()
	if err != nil {
		stderr := ""
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		return "", fmt.Errorf("codex CLI failed: %w — stderr: %s", err, truncateStr(stderr, 500))
	}

	raw := strings.TrimSpace(string(output))
	if raw == "" {
		return "", fmt.Errorf("codex CLI returned empty output")
	}

	return parseCodexJSONL(raw)
}

// parseCodexJSONL parses the JSONL event stream from `codex exec --json`.
func parseCodexJSONL(raw string) (string, error) {
	var responseText string
	var lastError string

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)
		switch eventType {
		case "item.completed":
			if item, ok := event["item"].(map[string]any); ok {
				if t, _ := item["type"].(string); t == "agent_message" {
					if text, _ := item["text"].(string); text != "" {
						responseText = text
					}
				}
			}
		case "turn.failed":
			if errObj, ok := event["error"].(map[string]any); ok {
				lastError, _ = errObj["message"].(string)
			}
		case "error":
			if msg, _ := event["message"].(string); msg != "" {
				lastError = msg
			}
		}
	}

	if lastError != "" && responseText == "" {
		return "", fmt.Errorf("codex error: %s", lastError)
	}

	return responseText, nil
}

// --- Shared CLI helpers ---

// buildCLIEnv returns an environment for CLI subprocesses.
// If apiKey is non-empty, it is injected as the given env var name.
func buildCLIEnv(apiKey, envVarName string) []string {
	env := os.Environ()

	// Ensure ~/.local/bin is in PATH
	home, _ := os.UserHomeDir()
	localBin := filepath.Join(home, ".local", "bin")
	pathUpdated := false
	for i, e := range env {
		if strings.HasPrefix(e, "PATH=") {
			path := strings.TrimPrefix(e, "PATH=")
			if !strings.Contains(path, localBin) {
				env[i] = "PATH=" + localBin + ":" + path
			}
			pathUpdated = true
			break
		}
	}
	if !pathUpdated {
		env = append(env, "PATH="+localBin+":/usr/local/bin:/usr/bin:/bin")
	}

	// Inject API key if provided
	if apiKey != "" && envVarName != "" {
		env = append(env, envVarName+"="+apiKey)
	}

	return env
}

// flattenMessages builds a single text from a message history.
func flattenMessages(messages []Message) string {
	var sb strings.Builder
	for _, m := range messages {
		fmt.Fprintf(&sb, "[%s]: %s\n\n", m.Role, m.Content)
	}
	return sb.String()
}

// flattenConversation builds a single text from a structured conversation.
func flattenConversation(messages []ConversationMessage) string {
	var sb strings.Builder
	for _, m := range messages {
		switch {
		case len(m.ToolResults) > 0:
			for _, tr := range m.ToolResults {
				fmt.Fprintf(&sb, "[tool_result %s]: %s\n\n", tr.ToolName, tr.Content)
			}
		case len(m.ToolCalls) > 0:
			if m.Content != "" {
				fmt.Fprintf(&sb, "[assistant]: %s\n\n", m.Content)
			}
			for _, tc := range m.ToolCalls {
				fmt.Fprintf(&sb, "[tool_call %s]: %s\n\n", tc.Name, tc.RawInput)
			}
		default:
			fmt.Fprintf(&sb, "[%s]: %s\n\n", m.Role, m.Content)
		}
	}
	return sb.String()
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
