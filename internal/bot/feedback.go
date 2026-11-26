package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/tmseidel/ai-git-bot/internal/feedback"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

// maxCalibration bounds the feedback a structured review is given.
const maxCalibration = 10

// handleFeedback answers a feedback command ("@bot feedback R12-F2 incorrect
// …"). It reports whether the comment was a feedback command; with feedback
// turned off such a comment is consumed without an answer.
func (h *WebhookHandler) handleFeedback(ctx context.Context, repoClient repo.Client, owner, repoName string, event *webhook.Event, bs botSettings) bool {
	cmd, ok := feedback.Parse(bs.identity.stripMentions(event.Comment.Body))
	if !ok {
		return false
	}
	prNum := resolvePRNumber(event)
	onPR := event.PullRequest != nil || (event.Issue != nil && event.Issue.IsPR)
	if !bs.feedbackEnabled {
		slog.Info("Feedback command ignored: feedback is off for this bot", "bot", bs.name)
		return true
	}
	reply := func(msg string) { repoClient.PostComment(ctx, owner, repoName, prNum, "**Feedback**: "+msg) }
	if !onPR || prNum == 0 {
		reply("Feedback works on pull requests, on the reviews I posted there.")
		return true
	}
	who := feedback.Author{Login: event.Comment.User, ID: event.Comment.UserID}
	if who.Login == "" {
		who.Login = event.Sender.Login
	}
	store := feedback.NewStore(h.db)
	scope := feedback.Scope{BotID: bs.id, Owner: owner, Repo: repoName, PRNumber: prNum}

	switch cmd.Kind {
	case feedback.KindInvalid:
		reply(fmt.Sprintf("I could not read that: %s. %s", cmd.Problem, feedback.Usage))
	case feedback.KindRecord:
		rec, replaced, duplicate, err := store.RecordVerdict(scope, cmd, who, event.Comment.ID)
		switch {
		case errors.Is(err, feedback.ErrUnknownReview):
			reply(fmt.Sprintf("%s is not a finished review of mine on this pull request.", fmt.Sprintf("R%d", cmd.Ref.Review)))
		case errors.Is(err, feedback.ErrUnknownFinding):
			reply(fmt.Sprintf("%s does not exist or was withdrawn.", cmd.Ref))
		case err != nil:
			slog.Error("Failed to record feedback", "err", err)
			reply("Something went wrong while recording that.")
		case duplicate:
			slog.Info("Feedback comment already recorded (redelivery)", "feedback", rec.ID)
		default:
			reply(feedback.RecordedText(rec, replaced))
		}
	case feedback.KindRetract:
		rec, err := store.Retract(scope, cmd.ID, who)
		if err != nil {
			reply(err.Error() + ".")
		} else {
			reply(fmt.Sprintf("Retracted FB%d (%s on %s).", rec.ID, rec.Verdict, rec.Target))
		}
	case feedback.KindList:
		recs, err := store.List(scope, cmd.Repository, 20)
		if err != nil {
			reply("Something went wrong while listing feedback.")
		} else {
			reply(feedback.ListText(recs, cmd.Repository))
		}
	case feedback.KindShow:
		rec, err := store.Get(scope, cmd.ID)
		if err != nil {
			reply(err.Error() + ".")
		} else {
			reply(feedback.ShowText(rec))
		}
	}
	return true
}

// calibration is the recent corrective feedback for a repository, for the
// structured review prompt.
func (h *WebhookHandler) calibration(bs botSettings, owner, repoName string) string {
	recs, err := feedback.NewStore(h.db).Calibration(bs.id, owner, repoName, maxCalibration)
	if err != nil {
		slog.Warn("Cannot load feedback calibration", "err", err)
		return ""
	}
	return feedback.CalibrationText(recs)
}
