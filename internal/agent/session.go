package agent

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

type SessionService struct {
	db *sql.DB
}

func NewSessionService(db *sql.DB) *SessionService {
	return &SessionService{db: db}
}

type AgentSessionRow struct {
	ID          int64
	RepoOwner   string
	RepoName    string
	IssueNumber int64
	IssueTitle  string
	BranchName  string
	PRNumber    int64
	Status      string
}

func (s *SessionService) Create(owner, repo string, issueNumber int64, title string) (*AgentSessionRow, error) {
	now := time.Now().UTC()
	result, err := s.db.Exec(`
		INSERT INTO agent_sessions (repo_owner, repo_name, issue_number, issue_title, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'IN_PROGRESS', ?, ?)
	`, owner, repo, issueNumber, title, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &AgentSessionRow{
		ID: id, RepoOwner: owner, RepoName: repo,
		IssueNumber: issueNumber, IssueTitle: title, Status: "IN_PROGRESS",
	}, nil
}

func (s *SessionService) GetByIssue(owner, repo string, issueNumber int64) (*AgentSessionRow, error) {
	var row AgentSessionRow
	var branchName, issueTitle sql.NullString
	var prNumber sql.NullInt64
	err := s.db.QueryRow(`
		SELECT id, repo_owner, repo_name, issue_number, issue_title, branch_name, pr_number, status
		FROM agent_sessions WHERE repo_owner = ? AND repo_name = ? AND issue_number = ?
	`, owner, repo, issueNumber).Scan(
		&row.ID, &row.RepoOwner, &row.RepoName, &row.IssueNumber,
		&issueTitle, &branchName, &prNumber, &row.Status,
	)
	if err != nil {
		return nil, err
	}
	if branchName.Valid {
		row.BranchName = branchName.String
	}
	if issueTitle.Valid {
		row.IssueTitle = issueTitle.String
	}
	if prNumber.Valid {
		row.PRNumber = prNumber.Int64
	}
	return &row, nil
}

func (s *SessionService) SetStatus(session *AgentSessionRow, status string) {
	session.Status = status
	s.db.Exec("UPDATE agent_sessions SET status = ?, updated_at = ? WHERE id = ?",
		status, time.Now().UTC(), session.ID)
}

func (s *SessionService) SetBranch(session *AgentSessionRow, branch string) {
	session.BranchName = branch
	s.db.Exec("UPDATE agent_sessions SET branch_name = ?, updated_at = ? WHERE id = ?",
		branch, time.Now().UTC(), session.ID)
}

func (s *SessionService) SetPR(session *AgentSessionRow, prNumber int64) {
	session.PRNumber = prNumber
	s.db.Exec("UPDATE agent_sessions SET pr_number = ?, updated_at = ? WHERE id = ?",
		prNumber, time.Now().UTC(), session.ID)
}

func (s *SessionService) AddMessage(session *AgentSessionRow, role, content string) {
	now := time.Now().UTC()
	s.db.Exec(
		"INSERT INTO conversation_messages (agent_session_id, role, content, created_at) VALUES (?, ?, ?, ?)",
		session.ID, role, content, now,
	)
}

func (s *SessionService) ToAiMessages(session *AgentSessionRow) []ai.Message {
	rows, err := s.db.Query(
		"SELECT role, content FROM conversation_messages WHERE agent_session_id = ? ORDER BY created_at ASC",
		session.ID,
	)
	if err != nil {
		slog.Error("Failed to load agent messages", "err", err)
		return nil
	}
	defer rows.Close()

	var messages []ai.Message
	for rows.Next() {
		var m ai.Message
		rows.Scan(&m.Role, &m.Content)
		messages = append(messages, m)
	}
	return messages
}
