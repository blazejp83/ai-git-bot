package web

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strings"

	"github.com/tmseidel/ai-git-bot/internal/auth"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
)

type Handlers struct {
	tpl      *Templates
	admin    *auth.AdminService
	sessions *auth.SessionManager
	db       *sql.DB
	enc      *encrypt.Service
}

func NewHandlers(tpl *Templates, admin *auth.AdminService, sessions *auth.SessionManager, db *sql.DB, enc *encrypt.Service) *Handlers {
	return &Handlers{tpl: tpl, admin: admin, sessions: sessions, db: db, enc: enc}
}

// --- Setup ---

func (h *Handlers) SetupPage(w http.ResponseWriter, r *http.Request) {
	required, err := h.admin.IsSetupRequired()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if !required {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}
	h.tpl.Render(w, "setup", map[string]any{"Error": ""})
}

func (h *Handlers) SetupSubmit(w http.ResponseWriter, r *http.Request) {
	required, _ := h.admin.IsSetupRequired()
	if !required {
		http.Redirect(w, r, "/login", http.StatusSeeOther)
		return
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	confirm := r.FormValue("confirmPassword")

	if username == "" {
		h.tpl.Render(w, "setup", map[string]any{"Error": "Username is required"})
		return
	}
	if len(password) < 8 {
		h.tpl.Render(w, "setup", map[string]any{"Error": "Password must be at least 8 characters"})
		return
	}
	if password != confirm {
		h.tpl.Render(w, "setup", map[string]any{"Error": "Passwords do not match"})
		return
	}

	if err := h.admin.CreateAdmin(username, password); err != nil {
		slog.Error("Failed to create admin", "err", err)
		h.tpl.Render(w, "setup", map[string]any{"Error": "Failed to create admin account"})
		return
	}

	slog.Info("Admin user created during initial setup", "username", username)
	http.Redirect(w, r, "/login?setup=ok", http.StatusSeeOther)
}

// --- Login ---

func (h *Handlers) LoginPage(w http.ResponseWriter, r *http.Request) {
	required, _ := h.admin.IsSetupRequired()
	if required {
		http.Redirect(w, r, "/setup", http.StatusSeeOther)
		return
	}
	data := map[string]any{
		"Error":   "",
		"Success": "",
		"Logout":  false,
	}
	if r.URL.Query().Get("error") != "" {
		data["Error"] = "Invalid username or password."
	}
	if r.URL.Query().Get("logout") != "" {
		data["Logout"] = true
	}
	if r.URL.Query().Get("setup") == "ok" {
		data["Success"] = "Admin account created successfully. Please login."
	}
	h.tpl.Render(w, "login", data)
}

func (h *Handlers) LoginSubmit(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")

	user, err := h.admin.Authenticate(username, password)
	if err != nil {
		slog.Error("Auth error", "err", err)
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}
	if user == nil {
		http.Redirect(w, r, "/login?error=1", http.StatusSeeOther)
		return
	}

	h.sessions.CreateSession(w, user.Username)
	http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
}

func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	h.sessions.DestroySession(w)
	http.Redirect(w, r, "/login?logout=1", http.StatusSeeOther)
}

// --- Dashboard ---

type dashboardData struct {
	ActiveNav           string
	Bots                []botRow
	TotalBots           int
	ActiveBots          int
	TotalWebhookCalls   int64
	TotalTokensSent     int64
	TotalTokensReceived int64
}

type botRow struct {
	ID                 int64
	Name               string
	Username           string
	AiIntegrationName  string
	GitIntegrationName string
	Enabled            bool
	WebhookCallCount   int64
	LastWebhookAt      string
	LastErrorMessage   string
	LastErrorAt        string
}

func (h *Handlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT b.id, b.name, b.username, b.enabled, b.webhook_call_count,
		       b.ai_tokens_sent, b.ai_tokens_received,
		       b.last_webhook_at, b.last_error_message, b.last_error_at,
		       ai.name, gi.name
		FROM bots b
		JOIN ai_integrations ai ON b.ai_integration_id = ai.id
		JOIN git_integrations gi ON b.git_integration_id = gi.id
		ORDER BY b.name
	`)
	if err != nil {
		slog.Error("Failed to query bots", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	data := dashboardData{ActiveNav: "dashboard"}
	for rows.Next() {
		var b botRow
		var enabled bool
		var tokensSent, tokensReceived int64
		var lastWebhook, lastError, lastErrorMsg sql.NullString
		err := rows.Scan(
			&b.ID, &b.Name, &b.Username, &enabled, &b.WebhookCallCount,
			&tokensSent, &tokensReceived,
			&lastWebhook, &lastErrorMsg, &lastError,
			&b.AiIntegrationName, &b.GitIntegrationName,
		)
		if err != nil {
			slog.Error("Failed to scan bot row", "err", err)
			continue
		}
		b.Enabled = enabled
		if lastWebhook.Valid {
			b.LastWebhookAt = lastWebhook.String
		}
		if lastErrorMsg.Valid {
			b.LastErrorMessage = lastErrorMsg.String
		}
		if lastError.Valid {
			b.LastErrorAt = lastError.String
		}
		data.Bots = append(data.Bots, b)
		data.TotalBots++
		if enabled {
			data.ActiveBots++
		}
		data.TotalWebhookCalls += b.WebhookCallCount
		data.TotalTokensSent += tokensSent
		data.TotalTokensReceived += tokensReceived
	}

	h.tpl.Render(w, "dashboard", data)
}
