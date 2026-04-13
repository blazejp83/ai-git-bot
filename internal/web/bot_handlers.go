package web

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/encrypt"
)

type BotHandlers struct {
	tpl *Templates
	db  *sql.DB
	enc *encrypt.Service
}

func NewBotHandlers(tpl *Templates, db *sql.DB, enc *encrypt.Service) *BotHandlers {
	return &BotHandlers{tpl: tpl, db: db, enc: enc}
}

type botListRow struct {
	ID               int64
	Name, Username   string
	AiName, GitName  string
	Enabled          bool
	AgentEnabled     bool
	WebhookPath      string
}

type botFormData struct {
	ActiveNav       string
	IsEdit          bool
	Bot             botFormRow
	AiIntegrations  []idNameRow
	GitIntegrations []idNameRow
}

type botFormRow struct {
	ID               int64
	Name             string
	Username         string
	Prompt           string
	WebhookSecret    string
	WebhookPath      string
	Enabled          bool
	AgentEnabled     bool
	AiIntegrationID  int64
	GitIntegrationID int64
}

type idNameRow struct {
	ID   int64
	Name string
}

func (h *BotHandlers) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT b.id, b.name, b.username, b.enabled, b.agent_enabled, b.webhook_secret,
		       ai.name, gi.name
		FROM bots b
		JOIN ai_integrations ai ON b.ai_integration_id = ai.id
		JOIN git_integrations gi ON b.git_integration_id = gi.id
		ORDER BY b.name
	`)
	if err != nil {
		http.Error(w, "Internal error", 500)
		return
	}
	defer rows.Close()

	var bots []botListRow
	for rows.Next() {
		var b botListRow
		var secret sql.NullString
		rows.Scan(&b.ID, &b.Name, &b.Username, &b.Enabled, &b.AgentEnabled, &secret, &b.AiName, &b.GitName)
		if secret.Valid {
			b.WebhookPath = "/api/webhook/" + secret.String
		}
		bots = append(bots, b)
	}

	h.tpl.Render(w, "bots/list", map[string]any{"ActiveNav": "bots", "Bots": bots})
}

func (h *BotHandlers) NewForm(w http.ResponseWriter, r *http.Request) {
	h.tpl.Render(w, "bots/form", botFormData{
		ActiveNav: "bots", IsEdit: false,
		Bot:             botFormRow{Enabled: true},
		AiIntegrations:  h.loadAiIntegrations(),
		GitIntegrations: h.loadGitIntegrations(),
	})
}

func (h *BotHandlers) EditForm(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)

	var b botFormRow
	var prompt, secret sql.NullString
	err := h.db.QueryRow(`SELECT id, name, username, prompt, webhook_secret, enabled, agent_enabled,
	       ai_integration_id, git_integration_id FROM bots WHERE id = ?`, id).
		Scan(&b.ID, &b.Name, &b.Username, &prompt, &secret, &b.Enabled, &b.AgentEnabled,
			&b.AiIntegrationID, &b.GitIntegrationID)
	if err != nil {
		http.Redirect(w, r, "/bots", http.StatusSeeOther)
		return
	}
	if prompt.Valid {
		b.Prompt = prompt.String
	}
	if secret.Valid {
		b.WebhookSecret = secret.String
		b.WebhookPath = "/api/webhook/" + secret.String
	}

	h.tpl.Render(w, "bots/form", botFormData{
		ActiveNav: "bots", IsEdit: true, Bot: b,
		AiIntegrations:  h.loadAiIntegrations(),
		GitIntegrations: h.loadGitIntegrations(),
	})
}

func (h *BotHandlers) Save(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	username := strings.TrimSpace(r.FormValue("username"))
	prompt := r.FormValue("prompt")
	aiID, _ := strconv.ParseInt(r.FormValue("aiIntegrationId"), 10, 64)
	gitID, _ := strconv.ParseInt(r.FormValue("gitIntegrationId"), 10, 64)
	enabled := r.FormValue("enabled") == "true"
	agentEnabled := r.FormValue("agentEnabled") == "true"
	idStr := r.FormValue("id")
	now := time.Now().UTC()

	if idStr != "" && idStr != "0" {
		id, _ := strconv.ParseInt(idStr, 10, 64)
		webhookSecret := r.FormValue("webhookSecret")
		h.db.Exec(`UPDATE bots SET name=?, username=?, prompt=?, enabled=?, agent_enabled=?,
			ai_integration_id=?, git_integration_id=?, webhook_secret=?, updated_at=? WHERE id=?`,
			name, username, nullStr(prompt), enabled, agentEnabled, aiID, gitID, nullStr(webhookSecret), now, id)
	} else {
		// Generate webhook secret for new bots
		secret := generateSecret()
		h.db.Exec(`INSERT INTO bots (name, username, prompt, webhook_secret, enabled, agent_enabled,
			ai_integration_id, git_integration_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			name, username, nullStr(prompt), secret, enabled, agentEnabled, aiID, gitID, now, now)
	}

	http.Redirect(w, r, "/bots", http.StatusSeeOther)
}

func (h *BotHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	h.db.Exec("DELETE FROM bots WHERE id = ?", id)
	http.Redirect(w, r, "/bots", http.StatusSeeOther)
}

func (h *BotHandlers) loadAiIntegrations() []idNameRow {
	rows, _ := h.db.Query("SELECT id, name FROM ai_integrations ORDER BY name")
	if rows == nil {
		return nil
	}
	defer rows.Close()
	var result []idNameRow
	for rows.Next() {
		var r idNameRow
		rows.Scan(&r.ID, &r.Name)
		result = append(result, r)
	}
	return result
}

func (h *BotHandlers) loadGitIntegrations() []idNameRow {
	rows, _ := h.db.Query("SELECT id, name FROM git_integrations ORDER BY name")
	if rows == nil {
		return nil
	}
	defer rows.Close()
	var result []idNameRow
	for rows.Next() {
		var r idNameRow
		rows.Scan(&r.ID, &r.Name)
		result = append(result, r)
	}
	return result
}

func generateSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}
