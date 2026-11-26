package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/structreview"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

func TestHandleFeedback(t *testing.T) {
	store := reviewStore(t)
	rv, _ := store.Create(1, "me", "app", 5, "sha", 0)
	store.AddFinding(rv.ID, structreview.Finding{Severity: structreview.Medium, Category: "tests", Title: "No test for the last page", Explanation: "e", Recommendation: "r"})
	store.SetAssessment(rv.ID, "ok", nil, nil, false)
	store.Finalize(rv.ID, structreview.DecisionApprove)

	h := &WebhookHandler{db: store.DB()}
	bs := botSettings{id: 1, feedbackEnabled: true, identity: newBotIdentity(repo.Account{ID: "42", Login: "ai-bot"}, "")}
	p := &fakeGitProvider{}
	comment := func(id int64, body string) *webhook.Event {
		return &webhook.Event{PullRequest: &webhook.PullRequest{Number: 5},
			Comment: &webhook.Comment{ID: id, Body: body, User: "alice", UserID: "1"}}
	}

	if !h.handleFeedback(context.Background(), p, "me", "app", comment(10, "@ai-bot feedback R1-F1 irrelevant this module has no paging tests by design"), bs) {
		t.Fatal("a feedback command must be handled")
	}
	if len(p.comments) != 1 || !strings.Contains(p.comments[0], "Recorded FB1 — irrelevant on R1-F1 (by @alice).") {
		t.Errorf("reply = %v", p.comments)
	}
	h.handleFeedback(context.Background(), p, "me", "app", comment(10, "@ai-bot feedback R1-F1 irrelevant this module has no paging tests by design"), bs)
	if len(p.comments) != 1 {
		t.Error("a redelivered comment must not be answered twice")
	}
	h.handleFeedback(context.Background(), p, "me", "app", comment(11, "@ai-bot feedback R1-F1 wrong"), bs)
	if !strings.Contains(p.comments[len(p.comments)-1], "Usage:") {
		t.Errorf("invalid command reply = %q", p.comments[len(p.comments)-1])
	}
	if h.handleFeedback(context.Background(), p, "me", "app", comment(12, "@ai-bot your feedback was great"), bs) {
		t.Error("conversation must not be taken as a feedback command")
	}

	off := bs
	off.feedbackEnabled = false
	n := len(p.comments)
	if !h.handleFeedback(context.Background(), p, "me", "app", comment(13, "@ai-bot feedback list"), off) || len(p.comments) != n {
		t.Error("with feedback off a command is consumed without an answer")
	}
	issue := &webhook.Event{Issue: &webhook.Issue{Number: 3}, Comment: &webhook.Comment{ID: 14, Body: "@ai-bot feedback list", User: "alice"}}
	h.handleFeedback(context.Background(), p, "me", "app", issue, bs)
	if !strings.Contains(p.comments[len(p.comments)-1], "works on pull requests") {
		t.Errorf("issue reply = %q", p.comments[len(p.comments)-1])
	}

	// The recorded verdict calibrates the next structured review of the repository.
	prompt := structuredPrompt("t", "b", "diff", true, h.calibration(bs, "me", "app"))
	if !strings.Contains(prompt, `[irrelevant] "MEDIUM tests: No test for the last page" — reason: this module has no paging tests by design`) {
		t.Errorf("prompt lacks calibration:\n%s", prompt)
	}
	if strings.Contains(structuredPrompt("t", "b", "diff", true, h.calibration(bs, "me", "other")), "Reviewers gave this feedback") {
		t.Error("calibration must be per repository")
	}
}
