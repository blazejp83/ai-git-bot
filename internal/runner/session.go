package runner

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

// Session manages conversation state for a runner execution.
type Session struct {
	db        *sql.DB
	SessionID int64
}

// CreateSession creates a new runner session in the database.
func CreateSession(db *sql.DB, mode Mode, owner, repo string, targetNumber int64, targetType, systemPrompt string) (*Session, error) {
	now := time.Now().UTC()
	result, err := db.Exec(`
		INSERT INTO runner_sessions (mode, repo_owner, repo_name, target_number, target_type, system_prompt, status, turn_count, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'IN_PROGRESS', 0, ?, ?)
	`, string(mode), owner, repo, targetNumber, targetType, systemPrompt, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := result.LastInsertId()
	return &Session{db: db, SessionID: id}, nil
}

// LoadSession loads an existing runner session.
func LoadSession(db *sql.DB, owner, repo, targetType string, targetNumber int64) (*Session, error) {
	var id int64
	err := db.QueryRow(`
		SELECT id FROM runner_sessions
		WHERE repo_owner = ? AND repo_name = ? AND target_type = ? AND target_number = ?
		ORDER BY created_at DESC LIMIT 1
	`, owner, repo, targetType, targetNumber).Scan(&id)
	if err != nil {
		return nil, err
	}
	return &Session{db: db, SessionID: id}, nil
}

// LoadMessages loads all conversation messages for this session,
// reconstructing them into the ConversationMessage format for the AI client.
func (s *Session) LoadMessages() []ai.ConversationMessage {
	rows, err := s.db.Query(`
		SELECT role, message_type, content, tool_call_id, tool_name, tool_input
		FROM runner_messages
		WHERE runner_session_id = ?
		ORDER BY created_at ASC
	`, s.SessionID)
	if err != nil {
		slog.Error("Failed to load runner messages", "err", err)
		return nil
	}
	defer rows.Close()

	var messages []ai.ConversationMessage
	var currentAssistant *ai.ConversationMessage
	var currentToolResults []ai.ToolResultMessage

	flush := func() {
		if currentAssistant != nil {
			messages = append(messages, *currentAssistant)
			currentAssistant = nil
		}
		if len(currentToolResults) > 0 {
			messages = append(messages, ai.ConversationMessage{
				Role:        "user",
				ToolResults: currentToolResults,
			})
			currentToolResults = nil
		}
	}

	for rows.Next() {
		var role, msgType, content string
		var toolCallID, toolName, toolInput sql.NullString
		rows.Scan(&role, &msgType, &content, &toolCallID, &toolName, &toolInput)

		switch {
		case role == "user" && msgType == "text":
			flush()
			messages = append(messages, ai.ConversationMessage{Role: "user", Content: content})

		case role == "assistant" && msgType == "text":
			flush()
			messages = append(messages, ai.ConversationMessage{Role: "assistant", Content: content})

		case role == "assistant" && msgType == "tool_call":
			if currentAssistant == nil {
				currentAssistant = &ai.ConversationMessage{Role: "assistant", Content: content}
			}
			var input map[string]any
			if toolInput.Valid {
				json.Unmarshal([]byte(toolInput.String), &input)
			}
			currentAssistant.ToolCalls = append(currentAssistant.ToolCalls, ai.ToolCall{
				ID:       toolCallID.String,
				Name:     toolName.String,
				Input:    input,
				RawInput: toolInput.String,
			})

		case role == "user" && msgType == "tool_result":
			currentToolResults = append(currentToolResults, ai.ToolResultMessage{
				ToolCallID: toolCallID.String,
				ToolName:   toolName.String,
				Content:    content,
			})
		}
	}

	flush()
	return messages
}

// SaveMessage persists a message to the database.
func (s *Session) SaveMessage(role, msgType, content, toolCallID, toolName, toolInput string) {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO runner_messages (runner_session_id, role, message_type, content, tool_call_id, tool_name, tool_input, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, s.SessionID, role, msgType, content, nullStr(toolCallID), nullStr(toolName), nullStr(toolInput), now)
	if err != nil {
		slog.Error("Failed to save runner message", "err", err)
	}
}

// IncrementTurns updates the turn count.
func (s *Session) IncrementTurns(count int) {
	s.db.Exec("UPDATE runner_sessions SET turn_count = ?, updated_at = ? WHERE id = ?",
		count, time.Now().UTC(), s.SessionID)
}

// SetStatus updates the session status.
func (s *Session) SetStatus(status string) {
	s.db.Exec("UPDATE runner_sessions SET status = ?, updated_at = ? WHERE id = ?",
		status, time.Now().UTC(), s.SessionID)
}

// SetBranch stores the branch name for implementation sessions.
func (s *Session) SetBranch(branch string) {
	s.db.Exec("UPDATE runner_sessions SET branch_name = ?, updated_at = ? WHERE id = ?",
		branch, time.Now().UTC(), s.SessionID)
}

// SetPR stores the PR number for implementation sessions.
func (s *Session) SetPR(prNumber int64) {
	s.db.Exec("UPDATE runner_sessions SET pr_number = ?, updated_at = ? WHERE id = ?",
		prNumber, time.Now().UTC(), s.SessionID)
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
