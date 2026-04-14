package review

import (
	"database/sql"
	"log/slog"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

const (
	maxMessagesAfterCompact = 4
	compactThresholdChars   = 50000
)

type SessionService struct {
	db *sql.DB
}

func NewSessionService(db *sql.DB) *SessionService {
	return &SessionService{db: db}
}

type Session struct {
	ID       int64
	Messages []SessionMessage
}

type SessionMessage struct {
	ID      int64
	Role    string
	Content string
}

func (s *SessionService) GetOrCreate(owner, repo string, prNumber int64, promptName string) (*Session, error) {
	var id int64
	err := s.db.QueryRow(
		"SELECT id FROM review_sessions WHERE repo_owner = ? AND repo_name = ? AND pr_number = ?",
		owner, repo, prNumber,
	).Scan(&id)

	if err == sql.ErrNoRows {
		now := time.Now().UTC()
		result, err := s.db.Exec(
			"INSERT INTO review_sessions (repo_owner, repo_name, pr_number, prompt_name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)",
			owner, repo, prNumber, promptName, now, now,
		)
		if err != nil {
			return nil, err
		}
		id, _ = result.LastInsertId()
		slog.Info("Created new review session", "id", id, "pr", prNumber, "repo", owner+"/"+repo)
		return &Session{ID: id}, nil
	}
	if err != nil {
		return nil, err
	}

	// Load messages
	session := &Session{ID: id}
	rows, err := s.db.Query(
		"SELECT id, role, content FROM conversation_messages WHERE session_id = ? ORDER BY created_at ASC", id,
	)
	if err != nil {
		return session, nil
	}
	defer rows.Close()

	for rows.Next() {
		var m SessionMessage
		rows.Scan(&m.ID, &m.Role, &m.Content)
		session.Messages = append(session.Messages, m)
	}

	slog.Info("Loaded existing review session", "id", id, "messages", len(session.Messages))
	return session, nil
}

func (s *SessionService) AddMessage(session *Session, role, content string) {
	now := time.Now().UTC()
	s.db.Exec(
		"INSERT INTO conversation_messages (session_id, role, content, created_at) VALUES (?, ?, ?, ?)",
		session.ID, role, content, now,
	)
	session.Messages = append(session.Messages, SessionMessage{Role: role, Content: content})
}

func (s *SessionService) Delete(owner, repo string, prNumber int64) {
	var id int64
	err := s.db.QueryRow(
		"SELECT id FROM review_sessions WHERE repo_owner = ? AND repo_name = ? AND pr_number = ?",
		owner, repo, prNumber,
	).Scan(&id)
	if err != nil {
		return
	}
	s.db.Exec("DELETE FROM conversation_messages WHERE session_id = ?", id)
	s.db.Exec("DELETE FROM review_sessions WHERE id = ?", id)
	slog.Info("Deleted review session", "id", id, "pr", prNumber)
}

func (s *SessionService) ToAiMessages(session *Session) []ai.Message {
	msgs := make([]ai.Message, len(session.Messages))
	for i, m := range session.Messages {
		msgs[i] = ai.Message{Role: m.Role, Content: m.Content}
	}
	return msgs
}

func (s *SessionService) CompactContextWindow(session *Session) {
	if len(session.Messages) <= maxMessagesAfterCompact {
		return
	}

	totalChars := 0
	for _, m := range session.Messages {
		totalChars += len(m.Content)
	}
	if totalChars < compactThresholdChars {
		return
	}

	removeCount := len(session.Messages) - maxMessagesAfterCompact
	slog.Info("Compacting session context window",
		"session", session.ID,
		"messages", len(session.Messages),
		"chars", totalChars,
		"removing", removeCount)

	// Count exchanges being removed
	userMsgs := 0
	for _, m := range session.Messages[:removeCount] {
		if m.Role == "user" {
			userMsgs++
		}
	}

	// Delete old messages from DB
	for _, m := range session.Messages[:removeCount] {
		if m.ID > 0 {
			s.db.Exec("DELETE FROM conversation_messages WHERE id = ?", m.ID)
		}
	}

	// Keep recent messages
	session.Messages = session.Messages[removeCount:]

	// Add context summary as first message
	summary := "[Previous conversation context was compacted. This discussion involves a code review. " +
		string(rune(userMsgs+'0')) + " previous exchanges were summarized to save context space.]"
	now := time.Now().UTC()
	result, err := s.db.Exec(
		"INSERT INTO conversation_messages (session_id, role, content, created_at) VALUES (?, 'user', ?, ?)",
		session.ID, summary, now,
	)
	if err == nil {
		id, _ := result.LastInsertId()
		session.Messages = append([]SessionMessage{{ID: id, Role: "user", Content: summary}}, session.Messages...)
	}
}
