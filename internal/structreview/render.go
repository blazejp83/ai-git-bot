package structreview

import (
	"fmt"
	"strings"
)

// Linker builds a web link to lines of a file at the reviewed commit.
type Linker func(path string, start, end int) string

// Outcome is how the decision was delivered.
type Outcome struct {
	Decision string // APPROVE, REQUEST_CHANGES or COMMENT
	Formal   bool   // delivered as a formal review decision
	Note     string // why it was not formal, if it should have been
}

func verdict(r *Review, o Outcome) string {
	switch {
	case r.Inconclusive:
		return "Inconclusive"
	case o.Decision == DecisionApprove:
		return "Approved"
	case o.Decision == DecisionRequestChanges:
		return "Changes requested"
	}
	return "Review"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

// Render writes the review comment from the stored review.
func Render(r *Review, link Linker, o Outcome) string {
	var b strings.Builder
	c := r.Counts()
	fmt.Fprintf(&b, "## AI Code Review · %s\n\n", r.Ref())
	fmt.Fprintf(&b, "**%s** · %s · %s · %s", verdict(r, o),
		plural(c[Blocker], "blocker", "blockers"), plural(c[Medium], "medium", "medium"), plural(c[Low], "low", "low"))
	if r.HeadSHA != "" {
		sha := r.HeadSHA
		if len(sha) > 10 {
			sha = sha[:10]
		}
		fmt.Fprintf(&b, " · commit `%s`", sha)
	}
	b.WriteString("\n")
	if o.Note != "" {
		fmt.Fprintf(&b, "\n> %s\n", o.Note)
	}

	fmt.Fprintf(&b, "\n### Assessment · %s-A\n\n", r.Ref())
	if strings.TrimSpace(r.Summary) == "" {
		b.WriteString("_No assessment was recorded._\n")
	} else {
		b.WriteString(strings.TrimSpace(r.Summary) + "\n")
	}

	b.WriteString("\n### Findings\n\n")
	active := r.Active()
	if len(active) == 0 {
		if r.Inconclusive {
			b.WriteString("No findings were recorded, but the review is inconclusive (see limitations).\n")
		} else {
			b.WriteString("No findings: nothing in this change needs attention.\n")
		}
	}
	for _, f := range active {
		fmt.Fprintf(&b, "#### %s · %s · %s\n\n", r.FindingRef(f.Ordinal), f.Severity, strings.TrimSpace(f.Title))
		var meta []string
		if len(f.Locations) > 0 {
			var where []string
			for _, l := range f.Locations {
				where = append(where, renderLocation(l, link))
			}
			meta = append(meta, "**Where:** "+strings.Join(where, ", "))
		}
		if f.Category != "" {
			meta = append(meta, "**Category:** "+f.Category)
		}
		if len(meta) > 0 {
			b.WriteString(strings.Join(meta, " · ") + "\n\n")
		}
		b.WriteString(strings.TrimSpace(f.Explanation) + "\n\n")
		if strings.TrimSpace(f.Impact) != "" {
			fmt.Fprintf(&b, "**Impact:** %s\n\n", strings.TrimSpace(f.Impact))
		}
		fmt.Fprintf(&b, "**Recommendation:** %s\n\n", strings.TrimSpace(f.Recommendation))
	}

	b.WriteString("### Coverage and limitations\n\n")
	if len(r.AreasReviewed) > 0 {
		fmt.Fprintf(&b, "**Reviewed:** %s\n\n", strings.Join(r.AreasReviewed, "; "))
	}
	if len(r.Limitations) > 0 {
		fmt.Fprintf(&b, "**Limitations:** %s\n\n", strings.Join(r.Limitations, "; "))
	} else {
		b.WriteString("**Limitations:** none stated.\n\n")
	}

	b.WriteString("---\n*Structured review by AI Git Bot. Findings are numbered for reference (e.g. `" + r.Ref() + "-F1`).*\n")
	return b.String()
}

func renderLocation(l Location, link Linker) string {
	lines := ""
	switch {
	case l.Start > 0 && l.End > l.Start:
		lines = fmt.Sprintf(" L%d–L%d", l.Start, l.End)
	case l.Start > 0:
		lines = fmt.Sprintf(" L%d", l.Start)
	}
	label := fmt.Sprintf("`%s`%s", l.Path, lines)
	if l.Side == "old" {
		return label + " (before the change)"
	}
	if link == nil {
		return label
	}
	return fmt.Sprintf("[%s](%s)", label, link(l.Path, l.Start, l.End))
}
