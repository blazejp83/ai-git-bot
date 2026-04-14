package review

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

const maxDiffCharsForContext = 60000

// Service handles code review business logic.
type Service struct {
	repoClient  repo.Client
	aiClient    ai.Client
	sessions    *SessionService
	botUsername string
	promptText  string
}

func NewService(repoClient repo.Client, aiClient ai.Client, sessions *SessionService, botUsername, promptText string) *Service {
	return &Service{
		repoClient:  repoClient,
		aiClient:    aiClient,
		sessions:    sessions,
		botUsername: botUsername,
		promptText:  promptText,
	}
}

func (s *Service) ReviewPullRequest(ctx context.Context, event *webhook.Event) {
	owner := event.Repo.Owner
	repoName := event.Repo.Name
	pr := event.PullRequest
	if pr == nil {
		return
	}

	slog.Info("Starting code review", "pr", pr.Number, "title", pr.Title, "repo", owner+"/"+repoName)

	diff, err := s.repoClient.GetPRDiff(ctx, owner, repoName, pr.Number)
	if err != nil || diff == "" {
		slog.Warn("No diff found for PR", "pr", pr.Number, "err", err)
		return
	}

	session, err := s.sessions.GetOrCreate(owner, repoName, pr.Number, "")
	if err != nil {
		slog.Error("Failed to get/create session", "err", err)
		return
	}

	var review string
	if len(session.Messages) == 0 {
		// Initial review
		review, err = s.aiClient.ReviewDiff(ctx, ai.ReviewRequest{
			PRTitle:      pr.Title,
			PRBody:       pr.Body,
			Diff:         diff,
			SystemPrompt: s.promptText,
		})
		if err != nil {
			slog.Error("AI review failed", "err", err)
			return
		}
		s.sessions.AddMessage(session, "user", buildPRSummary(pr.Title, pr.Body))
		s.sessions.AddMessage(session, "assistant", review)
	} else {
		// Updated PR — use conversation context
		updateMsg := buildPRUpdateMessage(pr.Title, diff)
		history := s.sessions.ToAiMessages(session)
		review, err = s.aiClient.Chat(ctx, history, updateMsg, ai.ChatOpts{SystemPrompt: s.promptText})
		if err != nil {
			slog.Error("AI chat failed", "err", err)
			return
		}
		s.sessions.AddMessage(session, "user", updateMsg)
		s.sessions.AddMessage(session, "assistant", review)
	}

	comment := formatReviewComment(review)
	if err := s.repoClient.PostReviewComment(ctx, owner, repoName, pr.Number, comment); err != nil {
		slog.Error("Failed to post review comment", "err", err)
	}

	s.sessions.CompactContextWindow(session)
	slog.Info("Code review completed", "pr", pr.Number)
}

func (s *Service) HandleBotCommand(ctx context.Context, event *webhook.Event) {
	owner := event.Repo.Owner
	repoName := event.Repo.Name
	comment := event.Comment
	if comment == nil {
		return
	}

	prNumber := resolvePRNumber(event)
	if prNumber == 0 {
		return
	}

	slog.Info("Handling bot command", "comment", comment.ID, "pr", prNumber)

	// Acknowledge
	s.repoClient.AddReaction(ctx, owner, repoName, comment.ID, "eyes")

	session, _ := s.sessions.GetOrCreate(owner, repoName, prNumber, "")

	// Seed context if empty
	if len(session.Messages) == 0 {
		diff, _ := s.repoClient.GetPRDiff(ctx, owner, repoName, prNumber)
		prCtx := buildPRContext(event, diff)
		s.sessions.AddMessage(session, "user", prCtx)
		s.sessions.AddMessage(session, "assistant", "I've reviewed the pull request context. How can I help you?")
	}

	history := s.sessions.ToAiMessages(session)
	response, err := s.aiClient.Chat(ctx, history, comment.Body, ai.ChatOpts{SystemPrompt: s.promptText})
	if err != nil {
		slog.Error("AI chat failed", "err", err)
		return
	}

	s.sessions.AddMessage(session, "user", comment.Body)
	s.sessions.AddMessage(session, "assistant", response)

	s.repoClient.PostComment(ctx, owner, repoName, prNumber, formatBotResponse(response))
	s.sessions.CompactContextWindow(session)
}

func (s *Service) HandleInlineComment(ctx context.Context, event *webhook.Event) {
	owner := event.Repo.Owner
	repoName := event.Repo.Name
	comment := event.Comment
	if comment == nil {
		return
	}

	prNumber := resolvePRNumber(event)
	if prNumber == 0 {
		return
	}

	slog.Info("Handling inline comment", "comment", comment.ID, "file", comment.Path, "pr", prNumber)

	s.repoClient.AddReaction(ctx, owner, repoName, comment.ID, "eyes")

	session, _ := s.sessions.GetOrCreate(owner, repoName, prNumber, "")

	if len(session.Messages) == 0 {
		diff, _ := s.repoClient.GetPRDiff(ctx, owner, repoName, prNumber)
		prCtx := buildPRContext(event, diff)
		s.sessions.AddMessage(session, "user", prCtx)
		s.sessions.AddMessage(session, "assistant", "I've reviewed the pull request context. How can I help you?")
	}

	contextMsg := buildInlineCommentContext(comment.Path, "", comment.Body)
	history := s.sessions.ToAiMessages(session)
	response, err := s.aiClient.Chat(ctx, history, contextMsg, ai.ChatOpts{SystemPrompt: s.promptText})
	if err != nil {
		slog.Error("AI chat failed for inline comment", "err", err)
		return
	}

	s.sessions.AddMessage(session, "user", contextMsg)
	s.sessions.AddMessage(session, "assistant", response)

	formatted := formatBotResponse(response)
	if comment.Line > 0 {
		err := s.repoClient.PostInlineComment(ctx, owner, repoName, prNumber, comment.Path, comment.Line, formatted)
		if err != nil {
			slog.Warn("Inline reply failed, falling back to regular comment", "err", err)
			s.repoClient.PostComment(ctx, owner, repoName, prNumber, formatted)
		}
	} else {
		s.repoClient.PostComment(ctx, owner, repoName, prNumber, formatted)
	}

	s.sessions.CompactContextWindow(session)
}

func (s *Service) HandleReviewSubmitted(ctx context.Context, event *webhook.Event) {
	owner := event.Repo.Owner
	repoName := event.Repo.Name
	pr := event.PullRequest
	if pr == nil {
		return
	}

	slog.Info("Handling review submitted", "pr", pr.Number)

	reviews, err := s.repoClient.GetReviews(ctx, owner, repoName, pr.Number)
	if err != nil || len(reviews) == 0 {
		return
	}

	// Find latest review
	latest := reviews[0]
	for _, r := range reviews[1:] {
		if r.ID > latest.ID {
			latest = r
		}
	}

	comments, err := s.repoClient.GetReviewComments(ctx, owner, repoName, pr.Number, latest.ID)
	if err != nil {
		return
	}

	botAlias := "@" + s.botUsername
	var mentionComments []repo.ReviewComment
	for _, c := range comments {
		if strings.Contains(c.Body, botAlias) {
			mentionComments = append(mentionComments, c)
		}
	}

	if len(mentionComments) == 0 {
		return
	}

	session, _ := s.sessions.GetOrCreate(owner, repoName, pr.Number, "")
	if len(session.Messages) == 0 {
		diff, _ := s.repoClient.GetPRDiff(ctx, owner, repoName, pr.Number)
		prCtx := buildPRContextFromPR(owner, repoName, pr, diff)
		s.sessions.AddMessage(session, "user", prCtx)
		s.sessions.AddMessage(session, "assistant", "I've reviewed the pull request context. How can I help you?")
	}

	for _, rc := range mentionComments {
		s.repoClient.AddReaction(ctx, owner, repoName, rc.ID, "eyes")

		contextMsg := buildInlineCommentContext(rc.Path, "", rc.Body)
		history := s.sessions.ToAiMessages(session)
		response, err := s.aiClient.Chat(ctx, history, contextMsg, ai.ChatOpts{SystemPrompt: s.promptText})
		if err != nil {
			slog.Error("AI chat failed for review comment", "comment", rc.ID, "err", err)
			continue
		}

		s.sessions.AddMessage(session, "user", contextMsg)
		s.sessions.AddMessage(session, "assistant", response)

		formatted := formatBotResponse(response)
		if rc.Line > 0 {
			err := s.repoClient.PostInlineComment(ctx, owner, repoName, pr.Number, rc.Path, rc.Line, formatted)
			if err != nil {
				s.repoClient.PostComment(ctx, owner, repoName, pr.Number, formatted)
			}
		} else {
			s.repoClient.PostComment(ctx, owner, repoName, pr.Number, formatted)
		}
	}

	s.sessions.CompactContextWindow(session)
}

func (s *Service) HandlePRClosed(ctx context.Context, event *webhook.Event) {
	if event.PullRequest == nil {
		return
	}
	s.sessions.Delete(event.Repo.Owner, event.Repo.Name, event.PullRequest.Number)
}

// --- helpers ---

func resolvePRNumber(event *webhook.Event) int64 {
	if event.Issue != nil && event.Issue.Number > 0 {
		return event.Issue.Number
	}
	if event.PullRequest != nil && event.PullRequest.Number > 0 {
		return event.PullRequest.Number
	}
	return 0
}

func truncateDiff(diff string) string {
	if len(diff) > maxDiffCharsForContext {
		return diff[:maxDiffCharsForContext] + "\n...(truncated)"
	}
	return diff
}

func buildPRSummary(title, body string) string {
	msg := "I opened a pull request titled '" + title + "'."
	if body != "" {
		msg += " Description: " + body
	}
	msg += " Please review it."
	return msg
}

func buildPRUpdateMessage(title, diff string) string {
	return "The pull request '" + title + "' has been updated with new changes. " +
		"Please review the updated diff:\n```diff\n" + truncateDiff(diff) + "\n```"
}

func buildPRContext(event *webhook.Event, diff string) string {
	title := ""
	body := ""
	if event.Issue != nil {
		title = event.Issue.Title
		body = event.Issue.Body
	} else if event.PullRequest != nil {
		title = event.PullRequest.Title
		body = event.PullRequest.Body
	}

	ctx := "This is a pull request. Title: " + title
	if body != "" {
		ctx += "\nDescription: " + body
	}
	if diff != "" {
		ctx += "\n\nDiff:\n```diff\n" + truncateDiff(diff) + "\n```"
	}
	return ctx
}

func buildPRContextFromPR(owner, repoName string, pr *webhook.PullRequest, diff string) string {
	ctx := fmt.Sprintf("This is a pull request in %s/%s.", owner, repoName)
	if pr.Title != "" {
		ctx += " Title: " + pr.Title
	}
	if pr.Body != "" {
		ctx += "\nDescription: " + pr.Body
	}
	if diff != "" {
		ctx += "\n\nDiff:\n```diff\n" + truncateDiff(diff) + "\n```"
	}
	return ctx
}

func buildInlineCommentContext(filePath, diffHunk, commentBody string) string {
	var sb strings.Builder
	sb.WriteString("Someone left an inline review comment on file `")
	sb.WriteString(filePath)
	sb.WriteString("`.\n\n")
	if diffHunk != "" {
		sb.WriteString("Code context (diff hunk):\n```diff\n")
		sb.WriteString(diffHunk)
		sb.WriteString("\n```\n\n")
	}
	sb.WriteString("Their comment/question:\n")
	sb.WriteString(commentBody)
	return sb.String()
}

func formatReviewComment(review string) string {
	return "## AI Code Review\n\n" + review + "\n\n---\n*Automated review by AI Git Bot*"
}

func formatBotResponse(response string) string {
	return "## Bot Response\n\n" + response + "\n\n---\n*Response by AI Git Bot*"
}
