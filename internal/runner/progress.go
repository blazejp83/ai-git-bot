package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/repo"
)

// ProgressReporter receives events from the runner loop.
type ProgressReporter interface {
	OnAssistantText(ctx context.Context, text string)
	OnToolCall(ctx context.Context, toolName string, input map[string]any)
	OnToolResult(ctx context.Context, toolName string, output string, err error)
	OnRateLimit(ctx context.Context, retryAfter time.Duration)
	OnUsageLimit(ctx context.Context, err *ai.UsageLimitError)
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

func (r *CommentReporter) OnUsageLimit(ctx context.Context, err *ai.UsageLimitError) {
	slog.Error("Usage limit reached", "type", err.ErrorType, "resets_at", err.ResetsAt, "plan", err.PlanType)

	var msg string
	switch err.ErrorType {
	case "usage_limit_reached":
		if !err.ResetsAt.IsZero() {
			resetStr := err.ResetsAt.Format("Jan 2 at 3:04 PM MST")
			msg = fmt.Sprintf("**Agent**: Usage limit reached. Your %s plan budget is exhausted.\n\nThe limit resets **%s**. The agent will stop now — re-trigger it after the reset.",
				err.PlanType, resetStr)
		} else {
			msg = "**Agent**: Usage limit reached. Your plan budget is exhausted. Please try again later."
		}
	case "insufficient_quota":
		msg = "**Agent**: Your API key has no remaining credits. Please add credits at https://platform.openai.com/account/billing"
	case "usage_not_included":
		msg = "**Agent**: This feature is not available on your current plan. Please upgrade your plan."
	default:
		msg = fmt.Sprintf("**Agent**: Usage limit error: %s", err.Message)
	}

	r.RepoClient.PostComment(ctx, r.Owner, r.Repo, r.Number, msg)
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
func (r *LogReporter) OnUsageLimit(ctx context.Context, err *ai.UsageLimitError) {
	slog.Error("Usage limit reached", "type", err.ErrorType, "resets_at", err.ResetsAt)
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
