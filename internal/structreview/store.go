package structreview

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// Store keeps structured reviews in SQLite.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// DB returns the store's database handle.
func (s *Store) DB() *sql.DB { return s.db }

func NewStore(db *sql.DB) *Store {
	return &Store{db: db, now: func() time.Time { return time.Now().UTC() }}
}

// Create starts a draft review of a pull request at headSHA.
func (s *Store) Create(botID int64, owner, repo string, pr int64, headSHA string, sessionID int64) (*Review, error) {
	now := s.now()
	res, err := s.db.Exec(`INSERT INTO review_records (bot_id, repo_owner, repo_name, pr_number, head_sha, runner_session_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, botID, owner, repo, pr, headSHA, sessionID, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Review{ID: id, BotID: botID, Owner: owner, Repo: repo, PRNumber: pr, HeadSHA: headSHA, State: "DRAFT"}, nil
}

// Get loads a review with all its findings, withdrawn ones included.
func (s *Store) Get(id int64) (*Review, error) {
	r := &Review{ID: id}
	var summary, areas, limits, decision sql.NullString
	var headSHA sql.NullString
	var botID sql.NullInt64
	err := s.db.QueryRow(`SELECT bot_id, repo_owner, repo_name, pr_number, head_sha, state, summary, areas_reviewed,
		limitations, inconclusive, decision FROM review_records WHERE id = ?`, id).
		Scan(&botID, &r.Owner, &r.Repo, &r.PRNumber, &headSHA, &r.State, &summary, &areas, &limits, &r.Inconclusive, &decision)
	if err != nil {
		return nil, fmt.Errorf("load review %d: %w", id, err)
	}
	r.BotID, r.HeadSHA, r.Summary, r.Decision = botID.Int64, headSHA.String, summary.String, decision.String
	json.Unmarshal([]byte(areas.String), &r.AreasReviewed)
	json.Unmarshal([]byte(limits.String), &r.Limitations)

	rows, err := s.db.Query(`SELECT ordinal, severity, COALESCE(category,''), title, explanation, COALESCE(impact,''),
		recommendation, locations, withdrawn FROM review_findings WHERE review_id = ? ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var f Finding
		var locs, sev string
		if err := rows.Scan(&f.Ordinal, &sev, &f.Category, &f.Title, &f.Explanation, &f.Impact, &f.Recommendation, &locs, &f.Withdrawn); err != nil {
			return nil, err
		}
		f.Severity = Severity(sev)
		json.Unmarshal([]byte(locs), &f.Locations)
		r.Findings = append(r.Findings, f)
	}
	return r, rows.Err()
}

// AddFinding stores a new finding and returns its ordinal (numbers are never reused).
func (s *Store) AddFinding(reviewID int64, f Finding) (int, error) {
	var next int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(ordinal),0)+1 FROM review_findings WHERE review_id = ?`, reviewID).Scan(&next); err != nil {
		return 0, err
	}
	locs, _ := json.Marshal(f.Locations)
	now := s.now()
	_, err := s.db.Exec(`INSERT INTO review_findings (review_id, ordinal, severity, category, title, explanation, impact,
		recommendation, locations, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		reviewID, next, string(f.Severity), f.Category, f.Title, f.Explanation, f.Impact, f.Recommendation, string(locs), now, now)
	return next, err
}

// UpdateFinding replaces an existing finding's content; a withdrawn finding is restored.
func (s *Store) UpdateFinding(reviewID int64, ordinal int, f Finding) error {
	locs, _ := json.Marshal(f.Locations)
	res, err := s.db.Exec(`UPDATE review_findings SET severity=?, category=?, title=?, explanation=?, impact=?, recommendation=?,
		locations=?, withdrawn=0, updated_at=? WHERE review_id=? AND ordinal=?`,
		string(f.Severity), f.Category, f.Title, f.Explanation, f.Impact, f.Recommendation, string(locs), s.now(), reviewID, ordinal)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("there is no finding F%d", ordinal)
	}
	return nil
}

// WithdrawFinding marks a finding withdrawn; its number stays taken.
func (s *Store) WithdrawFinding(reviewID int64, ordinal int) error {
	res, err := s.db.Exec(`UPDATE review_findings SET withdrawn=1, updated_at=? WHERE review_id=? AND ordinal=?`, s.now(), reviewID, ordinal)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("there is no finding F%d", ordinal)
	}
	return nil
}

// SetAssessment stores the overall assessment.
func (s *Store) SetAssessment(reviewID int64, summary string, areas, limitations []string, inconclusive bool) error {
	a, _ := json.Marshal(areas)
	l, _ := json.Marshal(limitations)
	_, err := s.db.Exec(`UPDATE review_records SET summary=?, areas_reviewed=?, limitations=?, inconclusive=?, updated_at=? WHERE id=?`,
		summary, string(a), string(l), inconclusive, s.now(), reviewID)
	return err
}

// Finalize records the decision and marks the review final.
func (s *Store) Finalize(reviewID int64, decision string) error {
	_, err := s.db.Exec(`UPDATE review_records SET state='FINAL', decision=?, updated_at=? WHERE id=?`, decision, s.now(), reviewID)
	return err
}

// RecordPublication stores one publish attempt and its outcome.
func (s *Store) RecordPublication(reviewID int64, event string, formal bool, status string, errText string) {
	s.db.Exec(`INSERT INTO review_publications (review_id, event, formal, status, error, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		reviewID, event, formal, status, errText, s.now())
}
