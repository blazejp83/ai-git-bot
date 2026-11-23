package runner

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

const (
	// compactThresholdChars is the conversation size (content plus tool
	// inputs) above which older turns are summarized.
	compactThresholdChars = 200_000
	// compactKeepMessages is how many recent messages stay verbatim.
	compactKeepMessages = 8
	// maxSummaryFiles bounds the edited-files list in the summary.
	maxSummaryFiles = 20
	// maxTaskChars bounds how much of the original task the summary repeats.
	maxTaskChars = 4000

	summaryPrefix = "[Previous agent session context was compacted to save space."
)

// fileEditingTools are the tools whose successful calls the summary records.
var fileEditingTools = map[string]bool{"write_file": true}

var exitCodeRe = regexp.MustCompile(`Exit code:\s*(-?\d+)`)

// compactMessages replaces older turns with one summary message once the
// conversation grows past compactThresholdChars, and keeps the most recent
// turns verbatim.
//
// The summary goes first and carries the task, the files already edited and
// the earlier requests, so the run does not redo finished work. The kept turns
// start on an assistant turn (never on an orphaned tool result), and their
// thinking blocks are dropped: they were produced with the full history in
// front of them, and current Claude models reject them once earlier turns are
// replaced. The session in the database keeps the full history.
func compactMessages(messages []ai.ConversationMessage, mode Mode, task string) ([]ai.ConversationMessage, bool) {
	if len(messages) <= compactKeepMessages+1 || conversationChars(messages) < compactThresholdChars {
		return nil, false
	}

	removeCount := len(messages) - compactKeepMessages
	for removeCount > 1 && len(messages[removeCount].ToolResults) > 0 {
		removeCount--
	}
	// Never split an assistant turn from its results.
	if removeCount <= 1 {
		return nil, false
	}

	removed := messages[:removeCount]
	kept := make([]ai.ConversationMessage, 0, len(messages)-removeCount+1)
	summary := buildCompactionSummary(removed, mode, task)
	for _, m := range messages[removeCount:] {
		m.Thinking = nil
		kept = append(kept, m)
	}
	// Merge a leading user text turn into the summary so roles keep alternating.
	if len(kept) > 0 && kept[0].Role == "user" && len(kept[0].ToolResults) == 0 {
		summary += "\n\n" + kept[0].Content
		kept = kept[1:]
	}
	return append([]ai.ConversationMessage{{Role: "user", Content: summary}}, kept...), true
}

func conversationChars(messages []ai.ConversationMessage) int {
	n := 0
	for _, m := range messages {
		n += len(m.Content)
		for _, tc := range m.ToolCalls {
			n += len(tc.RawInput) + len(marshalInput(tc.Input))
		}
		for _, tr := range m.ToolResults {
			n += len(tr.Content)
		}
	}
	return n
}

// initialPromptOf returns the run's original task: the first user message,
// unless that is already a compaction summary.
func initialPromptOf(messages []ai.ConversationMessage, fallback string) string {
	if len(messages) > 0 && messages[0].Role == "user" && len(messages[0].ToolResults) == 0 &&
		!isCompactionSummary(messages[0].Content) && messages[0].Content != "" {
		return messages[0].Content
	}
	return fallback
}

func isCompactionSummary(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), summaryPrefix)
}

func buildCompactionSummary(removed []ai.ConversationMessage, mode Mode, task string) string {
	users, assistants := 0, 0
	previous := ""
	results := map[string]string{}
	for _, m := range removed {
		switch {
		case m.Role == "assistant":
			assistants++
		case len(m.ToolResults) > 0:
			for _, tr := range m.ToolResults {
				res := tr.Content
				if tr.IsError {
					res = "Error: " + res
				}
				results[tr.ToolCallID] = res
			}
		default:
			users++
			if isCompactionSummary(m.Content) {
				previous = m.Content
			}
		}
	}

	// Files: those the previous summary listed, then successful edits.
	var files []string
	seen := map[string]bool{}
	addFile := func(f string) {
		f = strings.TrimSpace(f)
		if f != "" && !seen[f] && len(files) < maxSummaryFiles {
			seen[f] = true
			files = append(files, f)
		}
	}
	for _, line := range strings.Split(previous, "\n") {
		if rest, ok := strings.CutPrefix(line, "Files edited earlier in this session: "); ok {
			for _, f := range strings.Split(rest, ",") {
				addFile(f)
			}
		}
	}
	for _, m := range removed {
		for _, tc := range m.ToolCalls {
			if !fileEditingTools[tc.Name] || editFailed(results[tc.ID]) {
				continue
			}
			if p, _ := tc.Input["path"].(string); p != "" {
				addFile(p)
			} else if p, _ := tc.Input["file"].(string); p != "" {
				addFile(p)
			}
		}
	}

	// Earlier requests: user texts other than the task, summaries and notices.
	var requests []string
	for _, m := range removed {
		if m.Role != "user" || len(m.ToolResults) > 0 || m.Content == "" || m.Content == task ||
			isCompactionSummary(m.Content) || strings.HasPrefix(m.Content, "[SYSTEM]") {
			continue
		}
		requests = append(requests, m.Content)
	}
	if len(requests) > 5 {
		requests = requests[len(requests)-5:]
	}

	kind := "code review"
	if mode == ModeImplementation {
		kind = "coding agent"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s This is a %s session. %d previous exchanges were summarized.\n", summaryPrefix, kind, min(users, assistants))
	if task != "" {
		t := task
		if len(t) > maxTaskChars {
			t = t[:maxTaskChars] + "\n…(task shortened"
			if mode == ModeReview {
				t += "; the full diff is available through the pr_diff tool"
			}
			t += ")"
		}
		sb.WriteString("Original task:\n" + t + "\n")
	}
	if len(files) > 0 {
		sb.WriteString("Files edited earlier in this session: " + strings.Join(files, ", ") + "\n")
	}
	if len(requests) > 0 {
		sb.WriteString("Earlier requests:\n")
		for _, r := range requests {
			fmt.Fprintf(&sb, "- %q\n", brief(r, 160))
		}
	}
	if mode == ModeImplementation {
		sb.WriteString("Those files were edited earlier in this session; the edits are already in the workspace. " +
			"Read a file only if you need to change it again, and do not redo work that was already done.]")
	} else {
		sb.WriteString("The tool results of those exchanges are no longer shown. Re-read a file only if you need its exact content again, " +
			"and do not repeat explorations that were already done.]")
	}
	return sb.String()
}

// editFailed reports whether a file-editing call's result shows it did not
// take effect. A missing result is not a failure.
func editFailed(result string) bool {
	r := strings.TrimSpace(result)
	if strings.HasPrefix(r, "[") {
		if i := strings.Index(r, "]"); i >= 0 {
			r = strings.TrimSpace(r[i+1:])
		}
	}
	if strings.HasPrefix(r, "not executed") || strings.HasPrefix(r, "Error:") {
		return true
	}
	if m := exitCodeRe.FindStringSubmatch(r); m != nil && m[1] != "0" {
		return true
	}
	return false
}

// brief collapses whitespace and cuts text to max characters.
func brief(text string, maxLen int) string {
	t := strings.Join(strings.Fields(text), " ")
	if len([]rune(t)) > maxLen {
		t = strings.TrimSpace(string([]rune(t)[:maxLen])) + "…"
	}
	return t
}
