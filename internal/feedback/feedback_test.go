package feedback

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmseidel/ai-git-bot/internal/db"
	"github.com/tmseidel/ai-git-bot/internal/structreview"
)

func TestParse(t *testing.T) {
	cases := []struct {
		body string
		want Command
	}{
		{" feedback R12-F3 incorrect the loop is inclusive on purpose", Command{Kind: KindRecord, Ref: Ref{Review: 12, Finding: 3}, Verdict: Incorrect, Reason: "the loop is inclusive on purpose"}},
		{" feedback r12-a needs improvement\nToo vague.\nName the module.", Command{Kind: KindRecord, Ref: Ref{Review: 12, Assessment: true}, Verdict: NeedsImprovement, Reason: "Too vague.\nName the module."}},
		{" Feedback R7 useful", Command{Kind: KindRecord, Ref: Ref{Review: 7}, Verdict: Useful}},
		{" feedback list", Command{Kind: KindList}},
		{" feedback list repository", Command{Kind: KindList, Repository: true}},
		{" feedback retract FB4", Command{Kind: KindRetract, ID: 4}},
		{" feedback show fb9", Command{Kind: KindShow, ID: 9}},
	}
	for _, c := range cases {
		got, ok := Parse(c.body)
		if !ok || got != c.want {
			t.Errorf("Parse(%q) = %+v %v, want %+v", c.body, got, ok, c.want)
		}
	}
	for _, body := range []string{" feedback F3 incorrect", " feedback R12-F3 wrong", " feedback retract 4", " feedback"} {
		if got, ok := Parse(body); !ok || got.Kind != KindInvalid || got.Problem == "" {
			t.Errorf("Parse(%q) = %+v, want an explained invalid command", body, got)
		}
	}
	for _, body := range []string{" your feedback was great", " thanks!", "> feedback R1 useful\nI agree with the above", "```\nfeedback R1 useful\n```"} {
		if got, ok := Parse(body); ok {
			t.Errorf("Parse(%q) = %+v, want no command", body, got)
		}
	}
}

func setup(t *testing.T) (*Store, *structreview.Store, Scope) {
	t.Helper()
	conn, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	rs := structreview.NewStore(conn)
	r, _ := rs.Create(1, "me", "app", 5, "sha", 0)
	rs.AddFinding(r.ID, structreview.Finding{Severity: structreview.Medium, Category: "correctness", Title: "Off-by-one in paging", Explanation: "e", Recommendation: "r"})
	rs.AddFinding(r.ID, structreview.Finding{Severity: structreview.Low, Title: "Withdrawn one", Explanation: "e", Recommendation: "r"})
	rs.WithdrawFinding(r.ID, 2)
	rs.SetAssessment(r.ID, "ok", nil, nil, false)
	rs.Finalize(r.ID, structreview.DecisionApprove)
	return NewStore(conn), rs, Scope{BotID: 1, Owner: "me", Repo: "app", PRNumber: 5}
}

func TestRecordSupersedeRetractAndCalibration(t *testing.T) {
	s, _, sc := setup(t)
	alice, bob := Author{Login: "alice", ID: "1"}, Author{Login: "bob", ID: "2"}
	cmd := func(body string) Command { c, _ := Parse(body); return c }

	first, replaced, dup, err := s.RecordVerdict(sc, cmd(" feedback R1-F1 needs-improvement say which page"), alice, 100)
	if err != nil || replaced != 0 || dup || first.ID != 1 || first.Target != "R1-F1" {
		t.Fatalf("first: %+v %d %v %v", first, replaced, dup, err)
	}
	if !strings.Contains(RecordedText(first, 0), "Recorded FB1 — needs-improvement on R1-F1 (by @alice). Later reviews") {
		t.Errorf("reply = %q", RecordedText(first, 0))
	}
	// Redelivery of the same comment: no second record.
	if again, _, dup, _ := s.RecordVerdict(sc, cmd(" feedback R1-F1 needs-improvement say which page"), alice, 100); !dup || again.ID != 1 {
		t.Errorf("redelivery: dup=%v id=%d", dup, again.ID)
	}
	// Alice changes her mind: the new verdict replaces hers; Bob's stands beside it.
	second, replaced, _, _ := s.RecordVerdict(sc, cmd(" feedback R1-F1 incorrect pages start at 1 here"), alice, 101)
	if replaced != 1 {
		t.Errorf("replaced = %d, want 1", replaced)
	}
	s.RecordVerdict(sc, cmd(" feedback R1-F1 useful"), bob, 102)
	list, _ := s.List(sc, false, 20)
	if len(list) != 2 || list[0].Author != "bob" || list[1].ID != second.ID {
		t.Errorf("active list = %+v", list)
	}

	// Targets must exist on this PR's finished review.
	other := sc
	other.PRNumber = 6
	if _, _, _, err := s.RecordVerdict(other, cmd(" feedback R1-F1 useful"), alice, 0); err != ErrUnknownReview {
		t.Errorf("review on another PR: %v", err)
	}
	if _, _, _, err := s.RecordVerdict(sc, cmd(" feedback R1-F2 useful"), alice, 0); err != ErrUnknownFinding {
		t.Errorf("withdrawn finding: %v", err)
	}
	if _, _, _, err := s.RecordVerdict(sc, cmd(" feedback R9 useful"), alice, 0); err != ErrUnknownReview {
		t.Errorf("unknown review: %v", err)
	}

	// Only the author retracts.
	if _, err := s.Retract(sc, second.ID, bob); err == nil {
		t.Error("bob must not retract alice's feedback")
	}

	// Calibration: corrective verdicts on findings, with the finding's details.
	cal, _ := s.Calibration(1, "me", "app", 10)
	if len(cal) != 1 || cal[0].Verdict != Incorrect || cal[0].Title != "Off-by-one in paging" {
		t.Fatalf("calibration = %+v", cal)
	}
	text := CalibrationText(cal)
	if !strings.Contains(text, `[incorrect] "MEDIUM correctness: Off-by-one in paging" — reason: pages start at 1 here`) {
		t.Errorf("calibration text:\n%s", text)
	}

	if _, err := s.Retract(sc, second.ID, alice); err != nil {
		t.Error(err)
	}
	if cal, _ := s.Calibration(1, "me", "app", 10); len(cal) != 0 {
		t.Error("retracted feedback must not calibrate")
	}
	if got, _ := s.Get(sc, second.ID); got.Status != "retracted" || !strings.Contains(ShowText(got), "(retracted)") {
		t.Errorf("show = %q", ShowText(got))
	}
}
