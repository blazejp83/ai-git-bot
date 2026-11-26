package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/runner"
	"github.com/tmseidel/ai-git-bot/internal/workflow"
)

const maxDocsDiffChars = 60000

// docsPathAllowed reports whether the README sync may write a path: a
// Markdown file matching one of the patterns.
func docsPathAllowed(path string, patterns []string) bool {
	if !strings.HasSuffix(strings.ToLower(path), ".md") {
		return false
	}
	for _, p := range patterns {
		if runner.MatchesGlob(p, path) {
			return true
		}
	}
	return false
}

// writeMarkdownTool is the README sync's only write tool.
func writeMarkdownTool(patterns []string) runner.RegisteredTool {
	return runner.RegisteredTool{
		Def: ai.ToolDef{
			Name: "write_markdown",
			Description: "Replace the content of a Markdown documentation file. Only files matching these patterns may be written: " +
				strings.Join(patterns, ", ") + ". Keep the file's existing structure and style; change only what is outdated or missing.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"path":    map[string]any{"type": "string", "description": "Repository-relative path of the .md file"},
				"content": map[string]any{"type": "string", "description": "Complete new file content"},
			}, "required": []string{"path", "content"}},
		},
		Execute: func(_ context.Context, ws *runner.Workspace, in map[string]any) (string, error) {
			path, _ := in["path"].(string)
			content, _ := in["content"].(string)
			path = strings.TrimPrefix(strings.TrimSpace(path), "/")
			if !docsPathAllowed(path, patterns) {
				return "", fmt.Errorf("%s may not be edited: only .md files matching %s", path, strings.Join(patterns, ", "))
			}
			if err := ws.WriteFile(path, content); err != nil {
				return "", err
			}
			return fmt.Sprintf("Wrote %s (%d bytes)", path, len(content)), nil
		},
	}
}

// runReadmeSync updates the documentation a pull request makes outdated and
// delivers it as configured. auto runs (on a PR update) stay silent when
// there is nothing to change.
func (h *WebhookHandler) runReadmeSync(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, prNum int64, bs botSettings, instruction string, auto bool) {
	ctx, done := h.orchestrator().Start(ctx, runKey(bs.id, owner, repoName, prNum, workflow.ReadmeSync))
	defer done()
	say := func(msg string) { repoClient.PostComment(ctx, owner, repoName, prNum, "**README sync**: "+msg) }

	patterns := parseAllowlist(bs.workflows.Param(workflow.ReadmeSync, "patterns"))
	mode := bs.workflows.Param(workflow.ReadmeSync, "mode")

	head, err := repoClient.GetPRHead(ctx, owner, repoName, prNum)
	if err != nil || !head.Open {
		slog.Info("README sync skipped: PR not open or unreadable", "pr", prNum, "err", err)
		return
	}
	diff, err := repoClient.GetPRDiff(ctx, owner, repoName, prNum)
	if err != nil || diff == "" {
		slog.Warn("README sync: no diff", "pr", prNum, "err", err)
		return
	}
	ws, reviewedSHA, onHead, err := h.prepareReviewWorkspace(ctx, repoClient, bs.gitIntID, owner, repoName, prNum, "")
	if err != nil || !onHead {
		if ws != nil {
			ws.Cleanup()
		}
		if !auto {
			say("I could not check out the pull request's head, so I cannot update the documentation.")
		}
		return
	}
	defer ws.Cleanup()

	session, err := runner.CreateSession(h.db, runner.ModeDocs, owner, repoName, prNum, "pr", "")
	if err != nil {
		slog.Error("README sync: session", "err", err)
		return
	}
	r := runner.New(aiClient, ws, &runner.LogReporter{}, session, runner.Config{
		Mode:       runner.ModeDocs,
		MaxTurns:   bs.reviewTurns(20),
		MaxTokens:  h.cfg.AgentMaxTokens,
		PRDiff:     diff,
		ExtraTools: []runner.RegisteredTool{writeMarkdownTool(patterns)},
	})
	result, err := r.Run(ctx, readmePrompt(diff, patterns, instruction))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			slog.Info("README sync superseded by a newer run", "pr", prNum)
			session.SetStatus("CANCELLED")
			return
		}
		slog.Error("README sync failed", "err", err)
		if !auto {
			say("Something went wrong: " + err.Error())
		}
		session.SetStatus("FAILED")
		return
	}

	changes, _ := ws.Changes()
	if len(changes) == 0 {
		if !auto {
			say("The documentation already matches this pull request. " + truncate(result.Text, 500))
		}
		session.SetStatus("COMPLETED")
		return
	}
	// Do not publish for a pull request that moved on or closed meanwhile.
	if live, err := repoClient.GetPRHead(ctx, owner, repoName, prNum); err != nil || !live.Open || (reviewedSHA != "" && live.SHA != "" && live.SHA != reviewedSHA) {
		slog.Info("README sync not published: PR changed or closed", "pr", prNum)
		session.SetStatus("STALE")
		return
	}

	patch, err := ws.StagedDiff(ctx)
	if err != nil {
		say("Could not prepare the change: " + err.Error())
		session.SetStatus("FAILED")
		return
	}
	commentWithDiff := func(note string) {
		p := patch
		if len(p) > maxDocsDiffChars {
			p = p[:maxDocsDiffChars] + "\n…(diff shortened)"
		}
		msg := "Proposed documentation update:\n\n" + strings.TrimSpace(result.Text) + "\n\n```diff\n" + strings.TrimRight(p, "\n") + "\n```"
		if note != "" {
			msg = note + "\n\n" + msg
		}
		say(msg)
	}

	fork := head.SourceRepo != "" && !strings.EqualFold(head.SourceRepo, owner+"/"+repoName)
	if mode == "comment" || head.Branch == "" || fork {
		note := ""
		if mode != "comment" {
			note = "I cannot push to this pull request's branch (it lives in a fork), so here is the change as a diff."
		}
		commentWithDiff(note)
		session.SetStatus("COMPLETED")
		return
	}

	author, email := commitAuthor(bs)
	message := fmt.Sprintf("docs: update documentation for #%d\n\n%s", prNum, truncate(strings.TrimSpace(result.Text), 2000))
	if _, err := ws.Commit(ctx, message, author, email); err != nil {
		say("Could not commit the change: " + err.Error())
		session.SetStatus("FAILED")
		return
	}
	switch mode {
	case "commit":
		if err := ws.Push(ctx, head.Branch); err != nil {
			commentWithDiff(fmt.Sprintf("I could not push to `%s` (%v), so here is the change as a diff.", head.Branch, err))
			session.SetStatus("COMPLETED")
			return
		}
		say(fmt.Sprintf("Committed a documentation update to `%s`. %s", head.Branch, truncate(strings.TrimSpace(result.Text), 1000)))
	case "pr":
		branch, err := ws.PushNewBranch(ctx, fmt.Sprintf("ai-docs/pr-%d", prNum))
		if err != nil {
			commentWithDiff(fmt.Sprintf("I could not push a branch (%v), so here is the change as a diff.", err))
			session.SetStatus("COMPLETED")
			return
		}
		num, err := repoClient.CreatePR(ctx, owner, repoName, fmt.Sprintf("Docs update for %s", repoClient.FormatPRRef(prNum)),
			fmt.Sprintf("Updates the documentation for %s.\n\n%s\n\n---\n*Generated by AI Git Bot (README sync).*", repoClient.FormatPRRef(prNum), strings.TrimSpace(result.Text)),
			branch, head.Branch)
		if err != nil {
			say(fmt.Sprintf("Pushed `%s` but could not open a pull request: %v", branch, err))
			session.SetStatus("FAILED")
			return
		}
		say(fmt.Sprintf("Opened %s with the documentation update, into `%s`.", repoClient.FormatPRRef(num), head.Branch))
	}
	session.SetStatus("COMPLETED")
}

func readmePrompt(diff string, patterns []string, instruction string) string {
	var b strings.Builder
	b.WriteString("Update the project's Markdown documentation so it matches the changes in this pull request.\n\n")
	fmt.Fprintf(&b, "**Diff**:\n```diff\n%s\n```\n\n", truncateDiff(diff, 60000))
	fmt.Fprintf(&b, "You may edit only Markdown files matching: %s (with write_markdown). Read the current files first.\n", strings.Join(patterns, ", "))
	b.WriteString("Change only what this pull request makes outdated or leaves undocumented (new options, changed behaviour, removed features). " +
		"Keep each file's structure, tone and formatting. Do not rewrite sections that are still correct.\n")
	if instruction != "" {
		fmt.Fprintf(&b, "\n**Request from the reviewer**: %s\n", instruction)
	}
	b.WriteString("\nWhen done, call done with a one-paragraph summary of what you changed, or why nothing needed changing.")
	return b.String()
}
