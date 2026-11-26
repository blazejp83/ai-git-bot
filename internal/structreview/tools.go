package structreview

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/runner"
)

// FinishTool is the tool that completes a structured review.
const FinishTool = "review_complete"

const maxLocations = 5

// Tools returns the review tools for one draft review. pathKnown reports
// whether a path is part of the change or the repository, so findings
// cannot point at files that do not exist.
func Tools(store *Store, reviewID int64, pathKnown func(string) bool) []runner.RegisteredTool {
	locationSchema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path":       map[string]any{"type": "string", "description": "Repository-relative file path"},
			"start_line": map[string]any{"type": "integer", "description": "First line (1-based) in the new version of the file"},
			"end_line":   map[string]any{"type": "integer", "description": "Last line; omit for a single line"},
			"side":       map[string]any{"type": "string", "enum": []string{"new", "old"}, "description": "\"old\" for lines that the change removed; default \"new\""},
		},
		"required": []string{"path", "start_line"},
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	strList := func(desc string) map[string]any {
		return map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": desc}
	}
	schema := func(props map[string]any, required ...string) map[string]any {
		s := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			s["required"] = required
		}
		return s
	}

	return []runner.RegisteredTool{
		{
			Def: ai.ToolDef{
				Name: "review_upsert_finding",
				Description: "Record one finding of the review, or correct an earlier one by passing its id (e.g. \"F2\"). " +
					"One finding per distinct problem. Severity: BLOCKER = must be fixed before merging (bugs, data loss, security, broken builds); " +
					"MEDIUM = should be fixed (likely bugs in edge cases, missing tests for risky logic, maintainability problems); " +
					"LOW = optional improvements. Do not record style nits a linter would catch, or praise.",
				Parameters: schema(map[string]any{
					"id":             str("Id of an earlier finding to replace (e.g. \"F2\"); omit to add a new finding"),
					"severity":       map[string]any{"type": "string", "enum": []string{"BLOCKER", "MEDIUM", "LOW"}},
					"category":       str("Short category, e.g. correctness, security, performance, tests, maintainability"),
					"title":          str("One-line summary of the problem (under 120 characters)"),
					"explanation":    str("What is wrong and why, with evidence from the code"),
					"impact":         str("What happens if it is not fixed (optional)"),
					"recommendation": str("What to change"),
					"locations":      map[string]any{"type": "array", "items": locationSchema, "description": "Where the problem is (up to 5); empty for a change-wide finding"},
				}, "severity", "title", "explanation", "recommendation"),
			},
			Execute: func(_ context.Context, _ *runner.Workspace, in map[string]any) (string, error) {
				f, err := parseFinding(in, pathKnown)
				if err != nil {
					return "", err
				}
				r := &Review{ID: reviewID}
				if id, _ := in["id"].(string); strings.TrimSpace(id) != "" {
					ord, err := ParseFindingID(id)
					if err != nil {
						return "", err
					}
					if err := store.UpdateFinding(reviewID, ord, f); err != nil {
						return "", err
					}
					return fmt.Sprintf("Updated %s (%s): %s", r.FindingRef(ord), f.Severity, f.Title), nil
				}
				ord, err := store.AddFinding(reviewID, f)
				if err != nil {
					return "", err
				}
				return fmt.Sprintf("Recorded %s (%s): %s", r.FindingRef(ord), f.Severity, f.Title), nil
			},
		},
		{
			Def: ai.ToolDef{
				Name:        "review_remove_finding",
				Description: "Withdraw a finding that turned out to be wrong. Its number is not reused.",
				Parameters:  schema(map[string]any{"id": str("Finding id, e.g. \"F2\""), "reason": str("Why it is withdrawn")}, "id"),
			},
			Execute: func(_ context.Context, _ *runner.Workspace, in map[string]any) (string, error) {
				id, _ := in["id"].(string)
				ord, err := ParseFindingID(id)
				if err != nil {
					return "", err
				}
				if err := store.WithdrawFinding(reviewID, ord); err != nil {
					return "", err
				}
				return fmt.Sprintf("Withdrew %s.", (&Review{ID: reviewID}).FindingRef(ord)), nil
			},
		},
		{
			Def: ai.ToolDef{
				Name: "review_set_assessment",
				Description: "Record the overall assessment: a short summary of the change and its quality, the areas you reviewed, " +
					"and what you could not check. Set inconclusive if you could not review the change properly. Call it again to replace it.",
				Parameters: schema(map[string]any{
					"summary":        str("2-5 sentences: what the change does and your overall judgement"),
					"areas_reviewed": strList("What you examined, e.g. \"API handler changes\", \"new migration\""),
					"limitations":    strList("What you could not verify, e.g. \"did not run the integration tests\""),
					"inconclusive":   map[string]any{"type": "boolean", "description": "True if the review could not reach a judgement"},
				}, "summary"),
			},
			Execute: func(_ context.Context, _ *runner.Workspace, in map[string]any) (string, error) {
				summary, _ := in["summary"].(string)
				if strings.TrimSpace(summary) == "" {
					return "", errors.New("summary must not be empty")
				}
				inconclusive, _ := in["inconclusive"].(bool)
				if err := store.SetAssessment(reviewID, strings.TrimSpace(summary), stringList(in["areas_reviewed"]), stringList(in["limitations"]), inconclusive); err != nil {
					return "", err
				}
				return "Assessment recorded.", nil
			},
		},
		{
			Def: ai.ToolDef{
				Name:        "review_get_draft",
				Description: "Show the review as recorded so far (findings and assessment), to check it before completing.",
				Parameters:  schema(map[string]any{}),
			},
			Execute: func(_ context.Context, _ *runner.Workspace, _ map[string]any) (string, error) {
				r, err := store.Get(reviewID)
				if err != nil {
					return "", err
				}
				return Render(r, nil, Outcome{Decision: DecisionComment}), nil
			},
		},
		{
			Def: ai.ToolDef{
				Name:        FinishTool,
				Description: "Complete the review once every finding and the assessment are recorded. The server publishes it.",
				Parameters:  schema(map[string]any{}),
			},
			Execute: func(context.Context, *runner.Workspace, map[string]any) (string, error) { return "", nil },
		},
	}
}

// FinishCheck refuses to complete a review without an assessment.
func FinishCheck(store *Store, reviewID int64) func(map[string]any) (string, error) {
	return func(map[string]any) (string, error) {
		r, err := store.Get(reviewID)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(r.Summary) == "" {
			return "", errors.New("record the overall assessment with review_set_assessment first")
		}
		return "", nil
	}
}

func parseFinding(in map[string]any, pathKnown func(string) bool) (Finding, error) {
	text := func(k string) string { s, _ := in[k].(string); return strings.TrimSpace(s) }
	sev, err := ParseSeverity(text("severity"))
	if err != nil {
		return Finding{}, err
	}
	f := Finding{Severity: sev, Category: text("category"), Title: text("title"), Explanation: text("explanation"),
		Impact: text("impact"), Recommendation: text("recommendation")}
	switch {
	case f.Title == "":
		return Finding{}, errors.New("title must not be empty")
	case len([]rune(f.Title)) > 160:
		return Finding{}, errors.New("title is too long (at most 160 characters); keep it to one line")
	case f.Explanation == "":
		return Finding{}, errors.New("explanation must not be empty")
	case f.Recommendation == "":
		return Finding{}, errors.New("recommendation must not be empty")
	}
	locs, _ := in["locations"].([]any)
	if len(locs) > maxLocations {
		return Finding{}, fmt.Errorf("at most %d locations per finding; split it into several findings", maxLocations)
	}
	for i, raw := range locs {
		m, ok := raw.(map[string]any)
		if !ok {
			return Finding{}, fmt.Errorf("location %d must be an object", i+1)
		}
		l := Location{Path: strings.TrimPrefix(strings.TrimSpace(fmt.Sprint(m["path"])), "/"), Start: intOf(m["start_line"]), End: intOf(m["end_line"])}
		if side, _ := m["side"].(string); side == "old" {
			l.Side = "old"
		}
		switch {
		case l.Path == "" || l.Path == "<nil>":
			return Finding{}, fmt.Errorf("location %d needs a path", i+1)
		case l.Start < 1:
			return Finding{}, fmt.Errorf("location %d (%s): start_line must be 1 or more", i+1, l.Path)
		case l.End != 0 && l.End < l.Start:
			return Finding{}, fmt.Errorf("location %d (%s): end_line is before start_line", i+1, l.Path)
		case pathKnown != nil && !pathKnown(l.Path):
			return Finding{}, fmt.Errorf("location %d: %s is neither in the change nor in the repository", i+1, l.Path)
		}
		f.Locations = append(f.Locations, l)
	}
	return f, nil
}

func intOf(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// stringList reads a JSON array of strings, dropping blanks.
func stringList(v any) []string {
	list, _ := v.([]any)
	var out []string
	for _, item := range list {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}
