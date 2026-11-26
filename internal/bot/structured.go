package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/hooks"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/runner"
	"github.com/tmseidel/ai-git-bot/internal/structreview"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

// runStructuredReview reviews a pull request with the structured review
// tools, then publishes the stored review with a decision computed from its
// findings. It reports whether a review ran (see runReview).
func (h *WebhookHandler) runStructuredReview(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, prNum int64, pr *webhook.PullRequest, promptText string, bs botSettings, shellAllowlist []string) bool {
	h.emit(bs, owner, repoName, hooks.ReviewStarted, map[string]any{"pr": prNum, "structured": true})
	title, body, baseBranch := "", "", ""
	if pr != nil {
		title, body, baseBranch = pr.Title, pr.Body, pr.Base.RefName
	}
	diff, err := repoClient.GetPRDiff(ctx, owner, repoName, prNum)
	if err != nil || diff == "" {
		slog.Warn("No diff found for PR", "pr", prNum, "err", err)
		return false
	}
	ws, reviewedSHA, onHead, err := h.prepareReviewWorkspace(ctx, repoClient, bs.gitIntID, owner, repoName, prNum, baseBranch)
	if err != nil {
		slog.Error("Failed to create workspace for review", "err", err)
		return false
	}
	defer ws.Cleanup()

	session, err := runner.CreateSession(h.db, runner.ModeReview, owner, repoName, prNum, "pr", promptText)
	if err != nil {
		slog.Error("Failed to create runner session", "err", err)
		return false
	}
	store := structreview.NewStore(h.db)
	review, err := store.Create(bs.id, owner, repoName, prNum, reviewedSHA, session.SessionID)
	if err != nil {
		slog.Error("Failed to create review record", "err", err)
		session.SetStatus("FAILED")
		return false
	}

	changed := changedPaths(diff)
	pathKnown := func(p string) bool {
		if changed[p] {
			return true
		}
		abs, err := ws.Resolve(p)
		if err != nil {
			return false
		}
		_, err = os.Stat(abs)
		return err == nil
	}

	r := runner.New(aiClient, ws, &runner.CommentReporter{RepoClient: repoClient, Owner: owner, Repo: repoName, Number: prNum}, session, runner.Config{
		Mode:              runner.ModeReview,
		MaxTurns:          bs.reviewTurns(20),
		SystemPrompt:      promptText,
		MaxTokens:         h.cfg.AgentMaxTokens,
		ShellAllowlist:    shellAllowlist,
		ShellTimeout:      60,
		PRDiff:            diff,
		ExtraTools:        structreview.Tools(store, review.ID, pathKnown),
		FinishTool:        structreview.FinishTool,
		OnFinish:          structreview.FinishCheck(store, review.ID),
		RequireFinishTool: true,
	})
	result, err := r.Run(ctx, structuredPrompt(title, body, diff, onHead))
	if err != nil && errors.Is(err, context.Canceled) {
		slog.Info("Structured review superseded by a newer run", "pr", prNum, "review", review.ID)
		session.SetStatus("CANCELLED")
		return false
	}
	if err != nil {
		slog.Error("Structured review failed", "err", err, "review", review.ID)
		h.recordError(bs.id, "Review failed: "+err.Error())
		session.SetStatus("FAILED")
		h.emit(bs, owner, repoName, hooks.ReviewFailed, map[string]any{"pr": prNum, "structured": true, "error": err.Error()})
		return false
	}
	if headMoved(ctx, repoClient, owner, repoName, prNum, reviewedSHA) {
		session.SetStatus("STALE")
		h.emit(bs, owner, repoName, hooks.ReviewCompleted, map[string]any{"pr": prNum, "structured": true, "published": false, "reason": "the pull request moved on during the review"})
		return true
	}

	rv, err := store.Get(review.ID)
	if err != nil {
		slog.Error("Failed to load review", "err", err)
		session.SetStatus("FAILED")
		return false
	}
	if result.Status != runner.StatusComplete {
		// The turn limit ended the run: publish what was recorded, marked inconclusive.
		summary := rv.Summary
		if summary == "" {
			summary = "The review ran out of turns before an assessment was recorded."
		}
		limits := append(rv.Limitations, "the review hit its turn limit before completing, so findings may be missing")
		store.SetAssessment(rv.ID, summary, rv.AreasReviewed, limits, true)
		rv, _ = store.Get(review.ID)
	}

	decision := structreview.Decide(rv, bs.thresholds)
	var link structreview.Linker
	if reviewedSHA != "" {
		link = func(path string, start, end int) string {
			return repoClient.FileLink(owner, repoName, reviewedSHA, path, start, end)
		}
	}
	publishReview(ctx, repoClient, store, rv, link, decision, owner, repoName, prNum, reviewedSHA)
	store.Finalize(rv.ID, decision)
	session.SetStatus("COMPLETED")
	counts := rv.Counts()
	h.emit(bs, owner, repoName, hooks.ReviewCompleted, map[string]any{
		"pr": prNum, "structured": true, "published": true, "review": rv.Ref(), "decision": decision, "commit": reviewedSHA,
		"inconclusive": rv.Inconclusive, "findings": map[string]int{"blocker": counts[structreview.Blocker], "medium": counts[structreview.Medium], "low": counts[structreview.Low]},
	})
	slog.Info("Structured review completed", "pr", prNum, "review", rv.Ref(), "decision", decision,
		"findings", len(rv.Active()), "turns", result.TurnCount)
	return true
}

// publishReview delivers the review. A formal decision the provider refuses
// (e.g. approving one's own PR) is posted as a plain comment saying so; when
// the outcome is unknown (timeout) nothing is re-posted, to avoid duplicates.
func publishReview(ctx context.Context, repoClient repo.Client, store *structreview.Store, rv *structreview.Review, link structreview.Linker,
	decision, owner, repoName string, prNum int64, sha string) {
	if decision == structreview.DecisionComment {
		err := repoClient.PostReviewComment(ctx, owner, repoName, prNum, structreview.Render(rv, link, structreview.Outcome{Decision: decision}))
		store.RecordPublication(rv.ID, decision, false, statusOf(err), errText(err))
		return
	}

	body := structreview.Render(rv, link, structreview.Outcome{Decision: decision, Formal: true})
	posted, err := repoClient.SubmitReview(ctx, owner, repoName, prNum, body, repo.ReviewEvent(decision), sha)
	switch {
	case err == nil:
		store.RecordPublication(rv.ID, decision, true, "published", "")
	case posted:
		// The review is on the PR; only the formal decision failed (GitLab
		// cannot request changes).
		slog.Warn("Review posted, decision not applied", "review", rv.Ref(), "err", err)
		store.RecordPublication(rv.ID, decision, false, "posted-without-decision", err.Error())
	case repo.Answered(err) || errors.Is(err, repo.ErrDecisionUnsupported):
		note := fmt.Sprintf("Decision: %s. It could not be submitted as a formal review (%s), so it is posted as a comment.",
			decisionWords(decision), shortReason(err))
		fallback := structreview.Render(rv, link, structreview.Outcome{Decision: decision, Note: note})
		ferr := repoClient.PostReviewComment(ctx, owner, repoName, prNum, fallback)
		store.RecordPublication(rv.ID, decision, false, "fallback-"+statusOf(ferr), err.Error())
	default:
		slog.Warn("Review delivery outcome unknown; not re-posting", "review", rv.Ref(), "err", err)
		store.RecordPublication(rv.ID, decision, true, "unknown", err.Error())
	}
}

func decisionWords(decision string) string {
	switch decision {
	case structreview.DecisionApprove:
		return "approve"
	case structreview.DecisionRequestChanges:
		return "request changes"
	}
	return "comment"
}

func shortReason(err error) string {
	var he *repo.HTTPError
	if errors.As(err, &he) {
		return fmt.Sprintf("the provider answered HTTP %d", he.Status)
	}
	return truncate(err.Error(), 120)
}

func statusOf(err error) string {
	if err != nil {
		return "failed"
	}
	return "published"
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// changedPaths lists the files a unified diff touches (old and new paths).
func changedPaths(diff string) map[string]bool {
	paths := map[string]bool{}
	for _, line := range strings.Split(diff, "\n") {
		for _, prefix := range []string{"+++ b/", "--- a/"} {
			if p, ok := strings.CutPrefix(line, prefix); ok {
				paths[strings.TrimRight(p, "\r")] = true
			}
		}
	}
	return paths
}

func structuredPrompt(title, body, diff string, onHead bool) string {
	return fmt.Sprintf(`Review this pull request.

**Title**: %s
**Description**: %s

**Diff**:
`+"```diff\n%s\n```"+`

%s
Explore the repository to understand the changes: read related files, check tests, look for bugs, security problems and missing handling of edge cases. Use the pr_diff tool for exact line numbers.

How to report (only what you record with these tools is published; text you write is not):
1. Record each confirmed problem with review_upsert_finding as soon as you have checked it: one finding per problem, with the file and the line numbers in the new version of the file.
2. Correct a finding by calling review_upsert_finding with its id, or withdraw it with review_remove_finding if it turns out to be wrong.
3. Record your overall assessment with review_set_assessment, including what you reviewed and what you could not check.
4. Check the result with review_get_draft, then call review_complete.

If the change has no problems, record no findings: an assessment alone is a valid review.`,
		title, body, truncateDiff(diff, 60000), workspaceNote(onHead))
}
