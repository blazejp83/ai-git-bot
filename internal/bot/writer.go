package bot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/runner"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

// Issue workflows: what a bot does when assigned to an issue.
const (
	workflowImplement = "implement"
	workflowWriter    = "writer"
	workflowNone      = "none"
)

// Writer session statuses (runner_sessions.status).
const (
	writerWaiting      = "WAITING"       // asked questions, waits for answers
	writerIssueCreated = "ISSUE_CREATED" // created the refined issue
)

const (
	toolAskQuestions  = "ask_questions"
	toolCreateIssue   = "create_issue"
	maxQuestions      = 6
	writerTitlePrefix = "AI Created Issue: "
)

// writerOutcome is how a writer run ended.
type writerOutcome struct {
	questions []string
	lead      string // optional lead-in before the questions
	issue     *writerIssue
}

type writerIssue struct {
	Title, Background, ImplementationNotes string
	Requirements, Acceptance               []string
	Assumptions, OpenQuestions             []string
}

// writerTools are the two ways a writer run ends.
func writerTools() []runner.RegisteredTool {
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	list := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	finish := func(context.Context, *runner.Workspace, map[string]any) (string, error) { return "", nil }
	return []runner.RegisteredTool{
		{
			Def: ai.ToolDef{
				Name: toolAskQuestions,
				Description: "Ask the issue's author what you need to know before the issue can be specified. Only questions the code and the " +
					"discussion cannot answer; specific, one topic each. The run ends and continues when someone replies.",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{
					"questions": list(fmt.Sprintf("1 to %d questions", maxQuestions)),
					"context":   str("Optional one-sentence lead-in, e.g. what you understood so far"),
				}, "required": []string{"questions"}},
			},
			Execute: finish,
		},
		{
			Def: ai.ToolDef{
				Name: toolCreateIssue,
				Description: "Create the refined issue: a specification a developer or coding agent can implement without further questions. " +
					"Ground it in the code you read (name the files and functions involved).",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{
					"title":                str("Concise issue title (without any prefix)"),
					"background":           str("Why the change is needed and the current behaviour"),
					"requirements":         list("What must change, one requirement each"),
					"acceptance_criteria":  list("Testable conditions that show it is done"),
					"implementation_notes": str("Where in the code to make the change and how (optional)"),
					"assumptions":          list("Assumptions you made (optional)"),
					"open_questions":       list("Minor questions left for the implementer (optional)"),
				}, "required": []string{"title", "background", "requirements", "acceptance_criteria"}},
			},
			Execute: finish,
		},
	}
}

// writerFinishCheck validates the finishing call and records the outcome.
func writerFinishCheck(out *writerOutcome) func(string, map[string]any) (string, error) {
	return func(tool string, in map[string]any) (string, error) {
		text := func(k string) string { s, _ := in[k].(string); return strings.TrimSpace(s) }
		switch tool {
		case toolAskQuestions:
			qs := stringItems(in["questions"])
			if len(qs) == 0 || len(qs) > maxQuestions {
				return "", fmt.Errorf("ask 1 to %d questions", maxQuestions)
			}
			out.questions, out.lead = qs, text("context")
		case toolCreateIssue:
			wi := &writerIssue{Title: text("title"), Background: text("background"), ImplementationNotes: text("implementation_notes"),
				Requirements: stringItems(in["requirements"]), Acceptance: stringItems(in["acceptance_criteria"]),
				Assumptions: stringItems(in["assumptions"]), OpenQuestions: stringItems(in["open_questions"])}
			switch {
			case stripWriterPrefix(wi.Title) == "":
				return "", errors.New("title must not be empty")
			case wi.Background == "":
				return "", errors.New("background must not be empty")
			case len(wi.Requirements) == 0:
				return "", errors.New("list at least one requirement")
			case len(wi.Acceptance) == 0:
				return "", errors.New("list at least one acceptance criterion")
			}
			out.issue = wi
		}
		return "", nil
	}
}

var writerPrefixRe = regexp.MustCompile(`(?i)^(?:\s*AI\s+Created\s+Issue\s*:\s*)+`)

func stripWriterPrefix(title string) string {
	return strings.TrimSpace(writerPrefixRe.ReplaceAllString(title, ""))
}

// writerIssueTitle prefixes a title once, also when refining an issue the
// writer created itself.
func writerIssueTitle(title string) string { return writerTitlePrefix + stripWriterPrefix(title) }

func renderWriterIssue(wi *writerIssue, source int64) string {
	var b strings.Builder
	bullets := func(items []string, task bool) {
		for _, it := range items {
			if task {
				fmt.Fprintf(&b, "- [ ] %s\n", it)
			} else {
				fmt.Fprintf(&b, "- %s\n", it)
			}
		}
	}
	fmt.Fprintf(&b, "## Background\n\n%s\n\n## Requirements\n\n", wi.Background)
	bullets(wi.Requirements, false)
	b.WriteString("\n## Acceptance criteria\n\n")
	bullets(wi.Acceptance, true)
	if wi.ImplementationNotes != "" {
		fmt.Fprintf(&b, "\n## Implementation notes\n\n%s\n", wi.ImplementationNotes)
	}
	if len(wi.Assumptions) > 0 {
		b.WriteString("\n## Assumptions\n\n")
		bullets(wi.Assumptions, false)
	}
	if len(wi.OpenQuestions) > 0 {
		b.WriteString("\n## Open questions\n\n")
		bullets(wi.OpenQuestions, false)
	}
	fmt.Fprintf(&b, "\n---\n*Refined by AI Git Bot from #%d.*\n", source)
	return b.String()
}

func writerQuestionsComment(lead string, qs []string) string {
	var b strings.Builder
	b.WriteString("**AI Writer**: Before I write this up, I need a few answers.\n\n")
	if lead != "" {
		b.WriteString(lead + "\n\n")
	}
	for i, q := range qs {
		fmt.Fprintf(&b, "%d. %s\n", i+1, q)
	}
	b.WriteString("\nReply here (no need to mention me) and I'll continue.")
	return b.String()
}

func stringItems(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, item := range list {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// startWriter runs the writer for an assignment, unless a coding session
// already works on the issue or a writer run is still going.
func (h *WebhookHandler) startWriter(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, issue *webhook.Issue, promptText string, bs botSettings) {
	if _, _, ok := runner.LatestStatus(h.db, runner.ModeImplementation, owner, repoName, "issue", issue.Number); ok {
		repoClient.PostComment(ctx, owner, repoName, issue.Number,
			"**AI Writer**: This issue already has a coding session, so I won't rewrite it. Assign me to an issue that has not been implemented yet.")
		return
	}
	if writerBusy(h, owner, repoName, issue.Number) {
		slog.Info("Writer already running on issue", "issue", issue.Number)
		return
	}
	h.runWriter(ctx, aiClient, repoClient, owner, repoName, issue.Number, issue.Title, issue.Body, promptText, bs, false)
}

func writerBusy(h *WebhookHandler, owner, repoName string, issue int64) bool {
	status, updatedAt, ok := runner.LatestStatus(h.db, runner.ModeWriter, owner, repoName, "issue", issue)
	return ok && status == "IN_PROGRESS" && time.Since(updatedAt) < implementationStaleAfter
}

// routeToWriter continues a writer run that waits for answers: any reply on
// the issue continues it, no mention needed. A mention also resumes a run
// that failed or ran out of turns. It reports whether the comment was handled.
func (h *WebhookHandler) routeToWriter(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, event *webhook.Event, mentioned bool, promptText string, bs botSettings) bool {
	if bs.issueWorkflow != workflowWriter || event.Issue == nil || event.Issue.IsPR || event.PullRequest != nil {
		return false
	}
	status, _, ok := runner.LatestStatus(h.db, runner.ModeWriter, owner, repoName, "issue", event.Issue.Number)
	if !ok {
		return false
	}
	switch {
	case status == writerWaiting:
	case mentioned && (status == "FAILED" || status == "COMPLETED"):
	default:
		return false
	}
	if writerBusy(h, owner, repoName, event.Issue.Number) {
		return true
	}
	h.runWriter(ctx, aiClient, repoClient, owner, repoName, event.Issue.Number, event.Issue.Title, event.Issue.Body, promptText, bs, true)
	return true
}

func (h *WebhookHandler) runWriter(ctx context.Context, aiClient ai.Client, repoClient repo.Client, owner, repoName string, issueNum int64, title, body, promptText string, bs botSettings, continuation bool) {
	say := func(msg string) { repoClient.PostComment(ctx, owner, repoName, issueNum, msg) }
	if continuation {
		say("**AI Writer**: Thanks, continuing with your answers…")
	} else {
		say("**AI Writer**: I've been assigned to refine this issue into a specification. Reading the code…")
	}

	base, _ := repoClient.GetDefaultBranch(ctx, owner, repoName)
	if base == "" {
		base = "main"
	}
	ws, err := runner.NewWorkspace(ctx, h.cloneURL(bs.gitIntID, owner, repoName), owner, repoName, base)
	if err != nil {
		say("**AI Writer**: Failed to read the repository: " + err.Error())
		return
	}
	defer ws.Cleanup()

	session, err := runner.CreateSession(h.db, runner.ModeWriter, owner, repoName, issueNum, "issue", promptText)
	if err != nil {
		slog.Error("Failed to create writer session", "err", err)
		return
	}
	discussion := ""
	if comments, err := repoClient.ListIssueComments(ctx, owner, repoName, issueNum); err == nil {
		discussion = formatDiscussion(comments, bs.identity, 0)
	}

	var out writerOutcome
	r := runner.New(aiClient, ws, &runner.LogReporter{}, session, runner.Config{
		Mode:              runner.ModeWriter,
		MaxTurns:          bs.reviewTurns(20),
		SystemPrompt:      promptText,
		MaxTokens:         h.cfg.AgentMaxTokens,
		ExtraTools:        writerTools(),
		FinishTools:       []string{toolAskQuestions, toolCreateIssue},
		OnFinish:          writerFinishCheck(&out),
		RequireFinishTool: true,
	})
	result, err := r.Run(ctx, writerPrompt(issueNum, title, body, discussion, continuation))
	if err != nil {
		slog.Error("Writer run failed", "err", err)
		say("**AI Writer**: Something went wrong: " + err.Error() + ". Mention me to try again.")
		session.SetStatus("FAILED")
		return
	}

	switch {
	case out.issue != nil:
		newTitle := writerIssueTitle(out.issue.Title)
		num, err := repoClient.CreateIssue(ctx, owner, repoName, newTitle, renderWriterIssue(out.issue, issueNum))
		if err != nil {
			say("**AI Writer**: I wrote the specification but could not create the issue: " + err.Error())
			session.SetStatus("FAILED")
			return
		}
		session.SetPR(num) // the refined issue's number
		session.SetStatus(writerIssueCreated)
		say(fmt.Sprintf("**AI Writer**: Created a refined issue: #%d — %s", num, stripWriterPrefix(newTitle)))
		slog.Info("Writer created issue", "source", issueNum, "issue", num, "turns", result.TurnCount)
	case len(out.questions) > 0:
		say(writerQuestionsComment(out.lead, out.questions))
		session.SetStatus(writerWaiting)
	default:
		say("**AI Writer**: I need more context than I could gather in one go. Mention me (or reply) to continue.")
		session.SetStatus(writerWaiting)
	}
}

func writerPrompt(issueNum int64, title, body, discussion string, continuation bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Refine issue #%d into a specification that a developer or a coding agent can implement without further questions.\n\n", issueNum)
	fmt.Fprintf(&b, "**Title**: %s\n**Description**: %s\n", title, body)
	if discussion != "" {
		fmt.Fprintf(&b, "\n**Discussion** (oldest first):\n%s\n", discussion)
	}
	if continuation {
		b.WriteString("\nYou asked questions earlier; the answers are in the discussion above.\n")
	}
	b.WriteString(`
Read the code with list_files, read_file and rg to ground the specification: find where the change belongs and what it affects.

Then end the run with exactly one of these tools (text you write is not posted):
- ask_questions: if essential information is missing (intended behaviour, scope, acceptance) and neither the code nor the discussion answers it. Ask at most a few specific questions.
- create_issue: otherwise. Testable acceptance criteria; implementation notes that name the files and functions to change.`)
	return b.String()
}
