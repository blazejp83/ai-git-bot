package db

import (
	"database/sql"
	"time"
)

type AdminUser struct {
	ID           int64
	Username     string
	PasswordHash string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type AiIntegration struct {
	ID                    int64
	Name                  string
	ProviderType          string
	ApiURL                string
	ApiKey                string
	ApiVersion            sql.NullString
	Model                 string
	MaxTokens             int
	MaxDiffCharsPerChunk  int
	MaxDiffChunks         int
	RetryTruncatedChunkCh int
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type GitIntegration struct {
	ID           int64
	Name         string
	ProviderType string // GITEA, GITHUB, GITLAB, BITBUCKET
	URL          string
	Username     sql.NullString
	Token        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Bot struct {
	ID               int64
	Name             string
	Username         string
	Prompt           sql.NullString
	WebhookSecret    sql.NullString
	Enabled          bool
	AiIntegrationID  int64
	GitIntegrationID int64
	AgentEnabled     bool
	WebhookCallCount int64
	AiTokensSent     int64
	AiTokensReceived int64
	LastWebhookAt    sql.NullTime
	LastAiCallAt     sql.NullTime
	LastErrorMessage sql.NullString
	LastErrorAt      sql.NullTime
	CreatedAt        time.Time
	UpdatedAt        time.Time

	// Joined fields (not columns)
	AiIntegrationName  string
	GitIntegrationName string
}

func (b *Bot) WebhookPath() string {
	if !b.WebhookSecret.Valid {
		return ""
	}
	return "/api/webhook/" + b.WebhookSecret.String
}

type ReviewSession struct {
	ID         int64
	RepoOwner  string
	RepoName   string
	PRNumber   int64
	PromptName sql.NullString
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type ConversationMessage struct {
	ID        int64
	SessionID sql.NullInt64 // review_session FK
	AgentSID  sql.NullInt64 // agent_session FK
	Role      string
	Content   string
	CreatedAt time.Time
}

type AgentSession struct {
	ID          int64
	RepoOwner   string
	RepoName    string
	IssueNumber int64
	IssueTitle  sql.NullString
	BranchName  sql.NullString
	PRNumber    sql.NullInt64
	Status      string // IN_PROGRESS, PR_CREATED, UPDATING, COMPLETED, FAILED
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type AgentFileChange struct {
	ID             int64
	AgentSessionID int64
	Path           string
	Operation      string
	CommitSHA      sql.NullString
	CreatedAt      time.Time
}
