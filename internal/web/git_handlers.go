package web

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/encrypt"
)

type GitHandlers struct {
	tpl *Templates
	db  *sql.DB
	enc *encrypt.Service
}

func NewGitHandlers(tpl *Templates, db *sql.DB, enc *encrypt.Service) *GitHandlers {
	return &GitHandlers{tpl: tpl, db: db, enc: enc}
}

type gitIntegrationRow struct {
	ID           int64
	Name         string
	ProviderType string
	URL          string
	Username     string
	CreatedAt    string
}

func (h *GitHandlers) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query("SELECT id, name, provider_type, url, COALESCE(username,''), created_at FROM git_integrations ORDER BY name")
	if err != nil {
		http.Error(w, "Internal error", 500)
		return
	}
	defer rows.Close()

	var integrations []gitIntegrationRow
	for rows.Next() {
		var gi gitIntegrationRow
		rows.Scan(&gi.ID, &gi.Name, &gi.ProviderType, &gi.URL, &gi.Username, &gi.CreatedAt)
		integrations = append(integrations, gi)
	}

	h.tpl.Render(w, "git-integrations/list", map[string]any{
		"ActiveNav": "git-integrations", "Integrations": integrations,
	})
}

type gitFormData struct {
	ActiveNav     string
	IsEdit        bool
	Integration   gitFormRow
	ProviderTypes []string
}

type gitFormRow struct {
	ID           int64
	Name         string
	ProviderType string
	URL          string
	Username     string
}

func (h *GitHandlers) NewForm(w http.ResponseWriter, r *http.Request) {
	h.tpl.Render(w, "git-integrations/form", gitFormData{
		ActiveNav:     "git-integrations",
		ProviderTypes: []string{"GITEA", "GITHUB", "GITLAB", "BITBUCKET"},
	})
}

func (h *GitHandlers) EditForm(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)

	var gi gitFormRow
	var username sql.NullString
	err := h.db.QueryRow("SELECT id, name, provider_type, url, username FROM git_integrations WHERE id = ?", id).
		Scan(&gi.ID, &gi.Name, &gi.ProviderType, &gi.URL, &username)
	if err != nil {
		http.Redirect(w, r, "/git-integrations", http.StatusSeeOther)
		return
	}
	if username.Valid {
		gi.Username = username.String
	}

	h.tpl.Render(w, "git-integrations/form", gitFormData{
		ActiveNav: "git-integrations", IsEdit: true, Integration: gi,
		ProviderTypes: []string{"GITEA", "GITHUB", "GITLAB", "BITBUCKET"},
	})
}

func (h *GitHandlers) Save(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	providerType := r.FormValue("providerType")
	url := strings.TrimSpace(r.FormValue("url"))
	username := strings.TrimSpace(r.FormValue("username"))
	token := r.FormValue("token")
	idStr := r.FormValue("id")
	now := time.Now().UTC()

	if idStr != "" && idStr != "0" {
		id, _ := strconv.ParseInt(idStr, 10, 64)
		if token == "" {
			h.db.Exec("UPDATE git_integrations SET name=?, provider_type=?, url=?, username=?, updated_at=? WHERE id=?",
				name, providerType, url, nullStr(username), now, id)
		} else {
			encToken, _ := h.enc.Encrypt(token)
			h.db.Exec("UPDATE git_integrations SET name=?, provider_type=?, url=?, username=?, token=?, updated_at=? WHERE id=?",
				name, providerType, url, nullStr(username), encToken, now, id)
		}
	} else {
		encToken, _ := h.enc.Encrypt(token)
		h.db.Exec(`INSERT INTO git_integrations (name, provider_type, url, username, token, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			name, providerType, url, nullStr(username), encToken, now, now)
	}

	http.Redirect(w, r, "/git-integrations", http.StatusSeeOther)
}

func (h *GitHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)
	_, err := h.db.Exec("DELETE FROM git_integrations WHERE id = ?", id)
	if err != nil {
		slog.Error("Failed to delete git integration", "err", err)
	}
	http.Redirect(w, r, "/git-integrations", http.StatusSeeOther)
}
