package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/repo"
)

// ProgressReporter receives events from the runner loop.
type ProgressReporter interface {
	OnAssistantText(ctx context.Context, text string)
	OnToolCall(ctx context.Context, toolName string, input map[string]any)
	OnToolResult(ctx context.Context, toolName string, output string, err error)
	OnRateLimit(ctx context.Context, retryAfter time.Duration)
	OnComplete(ctx context.Context, result RunResult)
}

// CommentReporter posts progress as comments on a PR or issue.
type CommentReporter struct {
	RepoClient repo.Client
	Owner      string
	Repo       string
	Number     int64 // PR or issue number
	Verbose    bool  // if true, post every tool call as a comment
}

func (r *CommentReporter) OnAssistantText(ctx context.Context, text string) {
	if text == "" || len(text) < 20 {
		return
	}
	// Truncate very long reasoning
	if len(text) > 3000 {
		text = text[:3000] + "\n... (truncated)"
	}
	r.RepoClient.PostComment(ctx, r.Owner, r.Repo, r.Number,
		"**AI Agent** (thinking):\n\n"+text)
}

func (r *CommentReporter) OnToolCall(ctx context.Context, toolName string, input map[string]any) {
	slog.Info("Tool call", "tool", toolName, "input_keys", inputKeys(input))

	if !r.Verbose {
		return
	}

	var detail string
	switch toolName {
	case "read_file":
		detail = fmt.Sprintf("Reading `%s`", input["path"])
	case "write_file":
		detail = fmt.Sprintf("Writing `%s`", input["path"])
	case "search":
		detail = fmt.Sprintf("Searching for `%s`", input["pattern"])
	case "list_files":
		detail = fmt.Sprintf("Listing files in `%s`", input["path"])
	case "shell":
		detail = fmt.Sprintf("Running: `%s`", input["command"])
	default:
		detail = fmt.Sprintf("Calling `%s`", toolName)
	}

	r.RepoClient.PostComment(ctx, r.Owner, r.Repo, r.Number,
		"**Agent**: "+detail)
}

func (r *CommentReporter) OnToolResult(ctx context.Context, toolName string, output string, err error) {
	if err != nil {
		slog.Warn("Tool error", "tool", toolName, "err", err)
	}

	// Only post shell results that indicate failure
	if r.Verbose && toolName == "shell" && err == nil && strings.Contains(output, "Exit code:") {
		truncated := output
		if len(truncated) > 2000 {
			truncated = truncated[:2000] + "\n... (truncated)"
		}
		r.RepoClient.PostComment(ctx, r.Owner, r.Repo, r.Number,
			fmt.Sprintf("**Shell output**:\n```\n%s```", truncated))
	}
}

func (r *CommentReporter) OnRateLimit(ctx context.Context, retryAfter time.Duration) {
	slog.Warn("Rate limited, backing off", "retry_after", retryAfter)
	r.RepoClient.PostComment(ctx, r.Owner, r.Repo, r.Number,
		fmt.Sprintf("**Agent**: Rate limited, pausing for %v...", retryAfter.Round(time.Second)))
}

func (r *CommentReporter) OnComplete(ctx context.Context, result RunResult) {
	slog.Info("Runner complete", "status", result.Status, "turns", result.TurnCount)
}

// LogReporter just logs events (for testing or non-interactive use).
type LogReporter struct{}

func (r *LogReporter) OnAssistantText(ctx context.Context, text string) {
	slog.Debug("Assistant text", "chars", len(text))
}
func (r *LogReporter) OnToolCall(ctx context.Context, toolName string, input map[string]any) {
	slog.Info("Tool call", "tool", toolName)
}
func (r *LogReporter) OnToolResult(ctx context.Context, toolName string, output string, err error) {
	slog.Debug("Tool result", "tool", toolName, "output_chars", len(output))
}
func (r *LogReporter) OnRateLimit(ctx context.Context, retryAfter time.Duration) {
	slog.Warn("Rate limited", "retry_after", retryAfter)
}
func (r *LogReporter) OnComplete(ctx context.Context, result RunResult) {
	slog.Info("Complete", "status", result.Status)
}

func inputKeys(input map[string]any) []string {
	var keys []string
	for k := range input {
		keys = append(keys, k)
	}
	return keys
}
