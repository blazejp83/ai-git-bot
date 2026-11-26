// Package feedback records reviewers' verdicts on structured review output
// ("@bot feedback R12-F2 incorrect <reason>"), lists and retracts them, and
// hands recent verdicts back to later reviews as calibration.
package feedback

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Verdicts.
const (
	Useful           = "useful"
	Incorrect        = "incorrect"
	Irrelevant       = "irrelevant"
	NeedsImprovement = "needs-improvement"
)

const maxReason = 4000

// Command kinds.
const (
	KindRecord  = "record"
	KindRetract = "retract"
	KindList    = "list"
	KindShow    = "show"
	KindInvalid = "invalid"
)

// Command is a parsed feedback command.
type Command struct {
	Kind       string
	Ref        Ref    // record
	Verdict    string // record
	Reason     string // record
	ID         int64  // retract, show
	Repository bool   // list: the whole repository instead of this PR
	Problem    string // invalid: what is wrong
}

// Ref points at a structured review, one of its findings, or its assessment.
type Ref struct {
	Review     int64
	Finding    int // 0 = none
	Assessment bool
}

func (r Ref) String() string {
	switch {
	case r.Finding > 0:
		return fmt.Sprintf("R%d-F%d", r.Review, r.Finding)
	case r.Assessment:
		return fmt.Sprintf("R%d-A", r.Review)
	}
	return fmt.Sprintf("R%d", r.Review)
}

var (
	refRe = regexp.MustCompile(`(?i)^R(\d+)(?:-(?:F(\d+)|(A)))?$`)
	fbRe  = regexp.MustCompile(`(?i)^FB(\d+)$`)
	// A command starts with "feedback" once the bot's mention is removed:
	// "@bot your feedback was great" is conversation, not a command.
	commandRe = regexp.MustCompile(`(?i)^\s*feedback(?:\s+|$)`)
	fenceRe   = regexp.MustCompile("(?s)```.*?```")
)

// ParseRef reads "R12", "R12-F3" or "R12-A".
func ParseRef(s string) (Ref, bool) {
	m := refRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Ref{}, false
	}
	r := Ref{}
	r.Review, _ = strconv.ParseInt(m[1], 10, 64)
	if m[2] != "" {
		r.Finding, _ = strconv.Atoi(m[2])
	}
	r.Assessment = m[3] != ""
	return r, r.Review > 0
}

func normaliseVerdict(s string) string {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case Useful, Incorrect, Irrelevant, NeedsImprovement:
		return v
	case "needs_improvement", "needsimprovement":
		return NeedsImprovement
	}
	return ""
}

// clean drops fenced code and quoted lines: a quoted "@bot feedback …" is
// not a command.
func clean(body string) string {
	body = fenceRe.ReplaceAllString(body, "")
	var keep []string
	for _, l := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), ">") {
			continue
		}
		keep = append(keep, l)
	}
	return strings.Join(keep, "\n")
}

// Parse finds a feedback command in a comment whose mentions were already
// removed. ok is false when the comment is not a feedback command at all.
func Parse(body string) (Command, bool) {
	text := clean(body)
	loc := commandRe.FindStringIndex(text)
	if loc == nil {
		return Command{}, false
	}
	rest := strings.TrimSpace(text[loc[1]:])
	firstLine, more, _ := strings.Cut(rest, "\n")
	fields := strings.Fields(firstLine)
	if len(fields) == 0 {
		return Command{Kind: KindInvalid, Problem: "say what the feedback is about"}, true
	}
	switch strings.ToLower(fields[0]) {
	case "list":
		return Command{Kind: KindList, Repository: len(fields) > 1 && strings.HasPrefix(strings.ToLower(fields[1]), "repo")}, true
	case "retract", "show":
		kind := KindRetract
		if strings.ToLower(fields[0]) == "show" {
			kind = KindShow
		}
		if len(fields) < 2 {
			return Command{Kind: KindInvalid, Problem: "name the feedback, e.g. FB3"}, true
		}
		m := fbRe.FindStringSubmatch(fields[1])
		if m == nil {
			return Command{Kind: KindInvalid, Problem: fmt.Sprintf("%q is not a feedback id like FB3", fields[1])}, true
		}
		id, _ := strconv.ParseInt(m[1], 10, 64)
		return Command{Kind: kind, ID: id}, true
	}
	ref, ok := ParseRef(fields[0])
	if !ok {
		return Command{Kind: KindInvalid, Problem: fmt.Sprintf("%q is not a review reference like R12, R12-F3 or R12-A", fields[0])}, true
	}
	verdictWord, reasonStart := "", 2
	if len(fields) > 1 {
		verdictWord = fields[1]
		if strings.EqualFold(fields[1], "needs") && len(fields) > 2 && strings.EqualFold(fields[2], "improvement") {
			verdictWord, reasonStart = NeedsImprovement, 3
		}
	}
	verdict := normaliseVerdict(verdictWord)
	if verdict == "" {
		return Command{Kind: KindInvalid, Problem: "the verdict must be useful, incorrect, irrelevant or needs-improvement"}, true
	}
	reason := ""
	if len(fields) > reasonStart {
		reason = strings.Join(fields[reasonStart:], " ")
	}
	if m := strings.TrimSpace(more); m != "" {
		reason = strings.TrimSpace(reason + "\n" + m)
	}
	if len(reason) > maxReason {
		reason = reason[:maxReason]
	}
	return Command{Kind: KindRecord, Ref: ref, Verdict: verdict, Reason: reason}, true
}

// Usage is the help shown for a malformed command.
const Usage = "Usage: `feedback R12-F3 incorrect <why>` (verdicts: useful, incorrect, irrelevant, needs-improvement), " +
	"`feedback retract FB4`, `feedback show FB4`, `feedback list` or `feedback list repository`."

// Record is a stored verdict.
type Record struct {
	ID              int64
	Target          string
	Verdict, Reason string
	Author          string
	Status          string
	PRNumber        int64
	CreatedAt       time.Time
	// Finding details for calibration and display (empty for review-level feedback).
	Severity, Category, Title string
}

// Scope is where a command applies.
type Scope struct {
	BotID       int64
	Owner, Repo string
	PRNumber    int64
}

// Author identifies who gives feedback.
type Author struct {
	Login, ID string
}

func (a Author) same(login, id string) bool {
	if a.ID != "" && id != "" {
		return a.ID == id
	}
	return strings.EqualFold(a.Login, login)
}

// Store keeps feedback in SQLite.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// Errors a reply explains.
var (
	ErrUnknownReview  = errors.New("unknown review")
	ErrUnknownFinding = errors.New("unknown finding")
)

// RecordVerdict stores a verdict. It returns the new record, the id of the
// record it replaced (0 if none), and whether the comment was already
// recorded (a redelivered webhook).
func (s *Store) RecordVerdict(sc Scope, cmd Command, who Author, commentID int64) (rec Record, replaced int64, duplicate bool, err error) {
	if commentID > 0 {
		var existing int64
		if s.db.QueryRow(`SELECT id FROM review_feedback WHERE bot_id = ? AND comment_id = ?`, sc.BotID, commentID).Scan(&existing) == nil {
			r, err := s.Get(sc, existing)
			return r, 0, true, err
		}
	}
	// The review must be a finished review of this bot on this pull request.
	var state string
	if err := s.db.QueryRow(`SELECT state FROM review_records WHERE id = ? AND bot_id = ? AND repo_owner = ? AND repo_name = ? AND pr_number = ?`,
		cmd.Ref.Review, sc.BotID, sc.Owner, sc.Repo, sc.PRNumber).Scan(&state); err != nil || state != "FINAL" {
		return Record{}, 0, false, ErrUnknownReview
	}
	var ordinal sql.NullInt64
	if cmd.Ref.Finding > 0 {
		var withdrawn bool
		if err := s.db.QueryRow(`SELECT withdrawn FROM review_findings WHERE review_id = ? AND ordinal = ?`, cmd.Ref.Review, cmd.Ref.Finding).Scan(&withdrawn); err != nil || withdrawn {
			return Record{}, 0, false, ErrUnknownFinding
		}
		ordinal = sql.NullInt64{Int64: int64(cmd.Ref.Finding), Valid: true}
	}
	target := cmd.Ref.String()
	now := s.now()

	tx, err := s.db.Begin()
	if err != nil {
		return Record{}, 0, false, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id, author, COALESCE(author_id,'') FROM review_feedback
		WHERE bot_id = ? AND review_id = ? AND target = ? AND status = 'active'`, sc.BotID, cmd.Ref.Review, target)
	if err != nil {
		return Record{}, 0, false, err
	}
	for rows.Next() {
		var id int64
		var login, aid string
		if rows.Scan(&id, &login, &aid) == nil && who.same(login, aid) {
			replaced = id
		}
	}
	rows.Close()
	if replaced > 0 {
		tx.Exec(`UPDATE review_feedback SET status = 'superseded', updated_at = ? WHERE id = ?`, now, replaced)
	}
	var cid any
	if commentID > 0 {
		cid = commentID
	}
	res, err := tx.Exec(`INSERT INTO review_feedback (bot_id, repo_owner, repo_name, pr_number, review_id, finding_ordinal, target, verdict, reason,
		author, author_id, comment_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sc.BotID, sc.Owner, sc.Repo, sc.PRNumber, cmd.Ref.Review, ordinal, target, cmd.Verdict, cmd.Reason, who.Login, who.ID, cid, now, now)
	if err != nil {
		return Record{}, 0, false, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, 0, false, err
	}
	id, _ := res.LastInsertId()
	rec, err = s.Get(sc, id)
	return rec, replaced, false, err
}

// Retract withdraws the author's own active feedback.
func (s *Store) Retract(sc Scope, id int64, who Author) (Record, error) {
	rec, err := s.Get(sc, id)
	if err != nil {
		return Record{}, err
	}
	var login, aid string
	s.db.QueryRow(`SELECT author, COALESCE(author_id,'') FROM review_feedback WHERE id = ?`, id).Scan(&login, &aid)
	if !who.same(login, aid) {
		return Record{}, fmt.Errorf("FB%d was given by @%s; only they can retract it", id, login)
	}
	if rec.Status != "active" {
		return Record{}, fmt.Errorf("FB%d is already %s", id, rec.Status)
	}
	s.db.Exec(`UPDATE review_feedback SET status = 'retracted', updated_at = ? WHERE id = ?`, s.now(), id)
	rec.Status = "retracted"
	return rec, nil
}

const recordColumns = `f.id, f.target, f.verdict, COALESCE(f.reason,''), f.author, f.status, f.pr_number, f.created_at,
	COALESCE(rf.severity,''), COALESCE(rf.category,''), COALESCE(rf.title,'')`

const recordJoin = ` FROM review_feedback f LEFT JOIN review_findings rf ON rf.review_id = f.review_id AND rf.ordinal = f.finding_ordinal`

func scanRecords(rows *sql.Rows) ([]Record, error) {
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.ID, &r.Target, &r.Verdict, &r.Reason, &r.Author, &r.Status, &r.PRNumber, &r.CreatedAt,
			&r.Severity, &r.Category, &r.Title); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get loads one record of this bot and repository.
func (s *Store) Get(sc Scope, id int64) (Record, error) {
	rows, err := s.db.Query(`SELECT `+recordColumns+recordJoin+` WHERE f.id = ? AND f.bot_id = ? AND f.repo_owner = ? AND f.repo_name = ?`,
		id, sc.BotID, sc.Owner, sc.Repo)
	if err != nil {
		return Record{}, err
	}
	recs, err := scanRecords(rows)
	if err != nil {
		return Record{}, err
	}
	if len(recs) == 0 {
		return Record{}, fmt.Errorf("there is no FB%d in this repository", id)
	}
	return recs[0], nil
}

// List returns the active feedback on this pull request, or on the whole
// repository, newest first.
func (s *Store) List(sc Scope, repository bool, limit int) ([]Record, error) {
	q := `SELECT ` + recordColumns + recordJoin + ` WHERE f.bot_id = ? AND f.repo_owner = ? AND f.repo_name = ? AND f.status = 'active'`
	args := []any{sc.BotID, sc.Owner, sc.Repo}
	if !repository {
		q += ` AND f.pr_number = ?`
		args = append(args, sc.PRNumber)
	}
	rows, err := s.db.Query(q+` ORDER BY f.id DESC LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

// Calibration returns recent active corrective verdicts on findings in a
// repository (incorrect, irrelevant, needs-improvement), newest first.
func (s *Store) Calibration(botID int64, owner, repo string, limit int) ([]Record, error) {
	rows, err := s.db.Query(`SELECT `+recordColumns+recordJoin+` WHERE f.bot_id = ? AND f.repo_owner = ? AND f.repo_name = ?
		AND f.status = 'active' AND f.finding_ordinal IS NOT NULL AND f.verdict IN ('incorrect', 'irrelevant', 'needs-improvement')
		ORDER BY f.id DESC LIMIT ?`, botID, owner, repo, limit)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

func brief(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len([]rune(s)) > n {
		return string([]rune(s)[:n]) + "…"
	}
	return s
}

// CalibrationText renders corrective verdicts for a review prompt; "" when none.
func CalibrationText(recs []Record) string {
	if len(recs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Reviewers gave this feedback on findings in earlier reviews of this repository. Do not repeat findings like those marked " +
		"incorrect or irrelevant, unless this change really has that problem; apply the needs-improvement notes to how you write findings:\n")
	for _, r := range recs {
		what := r.Title
		if r.Severity != "" {
			what = fmt.Sprintf("%s %s: %s", r.Severity, r.Category, r.Title)
		}
		fmt.Fprintf(&b, "- [%s] %q", r.Verdict, brief(what, 160))
		if r.Reason != "" {
			fmt.Fprintf(&b, " — reason: %s", brief(r.Reason, 300))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// RecordedText is the reply to a recorded verdict.
func RecordedText(rec Record, replaced int64) string {
	msg := fmt.Sprintf("Recorded FB%d — %s on %s (by @%s).", rec.ID, rec.Verdict, rec.Target, rec.Author)
	if replaced > 0 {
		msg += fmt.Sprintf(" It replaces your earlier FB%d.", replaced)
	}
	if rec.Verdict != Useful && strings.Contains(rec.Target, "-F") {
		msg += " Later reviews of this repository take it into account."
	}
	return msg
}

// ListText renders a feedback list.
func ListText(recs []Record, repository bool) string {
	where := "this pull request"
	if repository {
		where = "this repository"
	}
	if len(recs) == 0 {
		return "No active feedback on " + where + "."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Active feedback on %s (newest first):\n\n| Id | Target | Verdict | By | Reason |\n| --- | --- | --- | --- | --- |\n", where)
	for _, r := range recs {
		reason := strings.ReplaceAll(brief(r.Reason, 120), "|", "\\|")
		fmt.Fprintf(&b, "| FB%d | %s | %s | @%s | %s |\n", r.ID, r.Target, r.Verdict, r.Author, reason)
	}
	return b.String()
}

// ShowText renders one record.
func ShowText(r Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**FB%d** — %s on %s by @%s, %s (%s).", r.ID, r.Verdict, r.Target, r.Author, r.CreatedAt.Format("2006-01-02"), r.Status)
	if r.Title != "" {
		fmt.Fprintf(&b, "\n\nFinding: %s · %s · %s", r.Severity, r.Category, r.Title)
	}
	if r.Reason != "" {
		fmt.Fprintf(&b, "\n\nReason: %s", r.Reason)
	}
	return b.String()
}
