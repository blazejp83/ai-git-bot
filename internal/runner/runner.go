package runner

import (
	"context"
	"log/slog"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

// Mode determines what tools are available and what the output is.
type Mode string

const (
	ModeReview         Mode = "review"         // read-only tools, output = comment
	ModeImplementation Mode = "implementation"  // read+write tools, output = PR
)

// Config holds runner configuration.
type Config struct {
	Mode           Mode
	MaxTurns       int
	SystemPrompt   string
	ModelOverride  string
	MaxTokens      int
	ShellAllowlist []string
	ShellTimeout   int // seconds
}

// RunStatus indicates how the runner finished.
type RunStatus string

const (
	StatusComplete RunStatus = "complete"
	StatusMaxTurns RunStatus = "max_turns"
	StatusError    RunStatus = "error"
)

// RunResult is the output of a runner execution.
type RunResult struct {
	Text      string
	Status    RunStatus
	TurnCount int
}

// Runner is the unified agentic loop.
type Runner struct {
	aiClient  ai.Client
	workspace *Workspace
	tools     *ToolRegistry
	reporter  ProgressReporter
	session   *Session
	config    Config
}

// New creates a runner with the given dependencies.
func New(aiClient ai.Client, workspace *Workspace, reporter ProgressReporter, session *Session, cfg Config) *Runner {
	shellTimeout := cfg.ShellTimeout
	if shellTimeout == 0 {
		shellTimeout = 300
	}
	tools := NewToolRegistry(cfg.Mode, cfg.ShellAllowlist, time.Duration(shellTimeout)*time.Second)

	return &Runner{
		aiClient:  aiClient,
		workspace: workspace,
		tools:     tools,
		reporter:  reporter,
		session:   session,
		config:    cfg,
	}
}

// Run executes the agentic loop.
func (r *Runner) Run(ctx context.Context, initialPrompt string) (RunResult, error) {
	messages := r.session.LoadMessages()

	// If no existing messages, start with the initial prompt
	if len(messages) == 0 {
		userMsg := ai.ConversationMessage{Role: "user", Content: initialPrompt}
		messages = append(messages, userMsg)
		r.session.SaveMessage("user", "text", initialPrompt, "", "", "")
	}

	toolDefs := r.tools.GetToolDefs()

	for turn := 0; turn < r.config.MaxTurns; turn++ {
		slog.Info("Runner turn", "turn", turn+1, "max", r.config.MaxTurns, "messages", len(messages))

		// Call AI with backoff
		response, err := r.callWithBackoff(ctx, messages, toolDefs)
		if err != nil {
			slog.Error("AI call failed", "turn", turn+1, "err", err)
			return RunResult{Status: StatusError, TurnCount: turn + 1}, err
		}

		// Persist and report assistant text
		text := response.Text()
		if text != "" {
			r.reporter.OnAssistantText(ctx, text)
		}

		// Save assistant message (with tool calls if any)
		r.saveAssistantMessage(response)

		// No tool calls → we're done
		if !response.HasToolCalls() {
			r.session.IncrementTurns(turn + 1)
			result := RunResult{Text: text, Status: StatusComplete, TurnCount: turn + 1}
			r.reporter.OnComplete(ctx, result)
			return result, nil
		}

		// Check for "done" tool
		for _, tc := range response.ToolCalls() {
			if tc.Name == "done" {
				resultText, _ := tc.Input["result"].(string)
				if resultText == "" {
					resultText = text
				}
				r.session.IncrementTurns(turn + 1)
				result := RunResult{Text: resultText, Status: StatusComplete, TurnCount: turn + 1}
				r.reporter.OnComplete(ctx, result)
				return result, nil
			}
		}

		// Execute each tool call
		var toolResults []ai.ToolResultMessage
		for _, tc := range response.ToolCalls() {
			r.reporter.OnToolCall(ctx, tc.Name, tc.Input)

			output, execErr := r.tools.Execute(ctx, r.workspace, tc.Name, tc.Input)

			isError := execErr != nil
			resultContent := output
			if isError {
				resultContent = "Error: " + execErr.Error()
			}

			r.reporter.OnToolResult(ctx, tc.Name, resultContent, execErr)

			// Persist tool result
			r.session.SaveMessage("user", "tool_result", resultContent, tc.ID, tc.Name, marshalInput(tc.Input))

			toolResults = append(toolResults, ai.ToolResultMessage{
				ToolCallID: tc.ID,
				ToolName:   tc.Name,
				Content:    resultContent,
				IsError:    isError,
			})
		}

		// Build updated conversation for next turn
		assistantMsg := ai.ConversationMessage{
			Role:      "assistant",
			Content:   text,
			ToolCalls: response.ToolCalls(),
		}
		toolResultMsg := ai.ConversationMessage{
			Role:        "user",
			ToolResults: toolResults,
		}
		messages = append(messages, assistantMsg, toolResultMsg)
	}

	// Exhausted turns
	r.session.IncrementTurns(r.config.MaxTurns)
	result := RunResult{Status: StatusMaxTurns, TurnCount: r.config.MaxTurns}
	r.reporter.OnComplete(ctx, result)
	return result, nil
}

func (r *Runner) saveAssistantMessage(response *ai.ChatResponse) {
	text := response.Text()
	toolCalls := response.ToolCalls()

	if len(toolCalls) > 0 {
		for _, tc := range toolCalls {
			r.session.SaveMessage("assistant", "tool_call", text, tc.ID, tc.Name, marshalInput(tc.Input))
			text = "" // only save text content with the first tool call
		}
	} else if text != "" {
		r.session.SaveMessage("assistant", "text", text, "", "", "")
	}
}

func (r *Runner) chatOpts() ai.ChatOpts {
	return ai.ChatOpts{
		SystemPrompt:     r.config.SystemPrompt,
		ModelOverride:    r.config.ModelOverride,
		MaxTokensOverride: r.config.MaxTokens,
	}
}

// (no helpers needed — time.Duration conversion is inline)
