// Package structreview implements structured reviews: the agent records
// findings and an assessment through tools, the server stores them, computes
// the review decision from them and renders the comment.
package structreview

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Severity of a finding.
type Severity string

const (
	Blocker Severity = "BLOCKER"
	Medium  Severity = "MEDIUM"
	Low     Severity = "LOW"
)

func (s Severity) rank() int {
	switch s {
	case Blocker:
		return 0
	case Medium:
		return 1
	}
	return 2
}

// ParseSeverity accepts the three severities in any case.
func ParseSeverity(s string) (Severity, error) {
	switch v := Severity(strings.ToUpper(strings.TrimSpace(s))); v {
	case Blocker, Medium, Low:
		return v, nil
	}
	return "", fmt.Errorf("severity must be BLOCKER, MEDIUM or LOW, got %q", s)
}

// Location is a line range in a file. Side "old" refers to the base version.
type Location struct {
	Path  string `json:"path"`
	Start int    `json:"start_line"`
	End   int    `json:"end_line,omitempty"`
	Side  string `json:"side,omitempty"`
}

// Finding is one issue the review found.
type Finding struct {
	Ordinal        int
	Severity       Severity
	Category       string
	Title          string
	Explanation    string
	Impact         string
	Recommendation string
	Locations      []Location
	Withdrawn      bool
}

// Review is a stored review with its findings.
type Review struct {
	ID            int64
	BotID         int64
	Owner, Repo   string
	PRNumber      int64
	HeadSHA       string
	State         string // DRAFT or FINAL
	Summary       string
	AreasReviewed []string
	Limitations   []string
	Inconclusive  bool
	Decision      string
	Findings      []Finding
}

// Ref is the review's reference, e.g. "R12".
func (r *Review) Ref() string { return "R" + strconv.FormatInt(r.ID, 10) }

// FindingRef is a finding's reference, e.g. "R12-F3".
func (r *Review) FindingRef(ordinal int) string { return fmt.Sprintf("%s-F%d", r.Ref(), ordinal) }

// Active returns the findings that were not withdrawn, most severe first,
// then in the order they were recorded.
func (r *Review) Active() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if !f.Withdrawn {
			out = append(out, f)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Severity.rank() != out[j].Severity.rank() {
			return out[i].Severity.rank() < out[j].Severity.rank()
		}
		return out[i].Ordinal < out[j].Ordinal
	})
	return out
}

// Counts returns the number of active findings per severity.
func (r *Review) Counts() map[Severity]int {
	c := map[Severity]int{}
	for _, f := range r.Active() {
		c[f.Severity]++
	}
	return c
}

// Thresholds are the most findings of each severity that still approve. Nil
// means no limit; all nil means the review never decides formally.
type Thresholds struct {
	MaxBlockers, MaxMedium, MaxLow *int
}

func (t Thresholds) any() bool { return t.MaxBlockers != nil || t.MaxMedium != nil || t.MaxLow != nil }

// Decision values, matching repo.ReviewEvent.
const (
	DecisionApprove        = "APPROVE"
	DecisionRequestChanges = "REQUEST_CHANGES"
	DecisionComment        = "COMMENT"
)

// Decide computes the decision from the stored findings only: every set
// threshold met → APPROVE, any exceeded → REQUEST_CHANGES. An inconclusive
// review, or one without thresholds, only comments.
func Decide(r *Review, t Thresholds) string {
	if r.Inconclusive || !t.any() {
		return DecisionComment
	}
	c := r.Counts()
	exceeds := func(limit *int, n int) bool { return limit != nil && n > *limit }
	if exceeds(t.MaxBlockers, c[Blocker]) || exceeds(t.MaxMedium, c[Medium]) || exceeds(t.MaxLow, c[Low]) {
		return DecisionRequestChanges
	}
	return DecisionApprove
}

// ParseFindingID accepts "F3", "f3", "3" or "R12-F3" and returns 3.
func ParseFindingID(s string) (int, error) {
	v := strings.ToUpper(strings.TrimSpace(s))
	if i := strings.LastIndex(v, "-"); i >= 0 {
		v = v[i+1:]
	}
	v = strings.TrimPrefix(v, "F")
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("finding id must look like F3, got %q", s)
	}
	return n, nil
}
