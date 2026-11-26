package web

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/encrypt"
	"github.com/tmseidel/ai-git-bot/internal/hooks"
)

// HookHandlers serve the outgoing hooks pages.
type HookHandlers struct {
	tpl   *Templates
	db    *sql.DB
	enc   *encrypt.Service
	hooks *hooks.Service
}

func NewHookHandlers(tpl *Templates, db *sql.DB, enc *encrypt.Service, svc *hooks.Service) *HookHandlers {
	return &HookHandlers{tpl: tpl, db: db, enc: enc, hooks: svc}
}

type hookRow struct {
	ID         int64
	Name, URL  string
	EventTypes []string
	Scope      string
	Enabled    bool
	LastStatus string
	LastAt     string
}

// List shows the endpoints with their latest delivery.
func (h *HookHandlers) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`SELECT e.id, e.name, e.url, e.event_types, e.enabled, COALESCE(b.name, ''), COALESCE(e.repo_filter, ''),
		COALESCE((SELECT status FROM hook_deliveries d WHERE d.endpoint_id = e.id ORDER BY d.id DESC LIMIT 1), ''),
		COALESCE((SELECT updated_at FROM hook_deliveries d WHERE d.endpoint_id = e.id ORDER BY d.id DESC LIMIT 1), '')
		FROM hook_endpoints e LEFT JOIN bots b ON b.id = e.bot_id ORDER BY e.name`)
	if err != nil {
		slog.Error("Failed to list hooks", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var list []hookRow
	for rows.Next() {
		var hr hookRow
		var types, bot, repo string
		var last any
		if err := rows.Scan(&hr.ID, &hr.Name, &hr.URL, &types, &hr.Enabled, &bot, &repo, &hr.LastStatus, &last); err != nil {
			continue
		}
		hr.EventTypes = strings.Split(types, ",")
		hr.Scope = scopeText(bot, repo)
		if t, ok := last.(time.Time); ok {
			hr.LastAt = t.Format("2006-01-02 15:04")
		} else if s, ok := last.(string); ok {
			hr.LastAt = s
		}
		list = append(list, hr)
	}
	h.tpl.Render(w, "hooks/list", map[string]any{"ActiveNav": "hooks", "Hooks": list, "Notice": r.URL.Query().Get("notice")})
}

func scopeText(bot, repo string) string {
	parts := []string{}
	if bot != "" {
		parts = append(parts, "bot "+bot)
	}
	if repo != "" {
		parts = append(parts, "repos "+repo)
	}
	if len(parts) == 0 {
		return "all bots and repositories"
	}
	return strings.Join(parts, ", ")
}

type hookForm struct {
	ID         int64
	Name, URL  string
	Events     map[string]bool
	BotID      int64
	RepoFilter string
	SkipTLS    bool
	Enabled    bool
	HasSecret  bool
	HasHeaders bool
}

type hookFormData struct {
	ActiveNav  string
	IsEdit     bool
	Hook       hookForm
	EventTypes []string
	Bots       []idNameRow
	Error      string
}

func (h *HookHandlers) formData(f hookForm, isEdit bool, errMsg string) hookFormData {
	var bots []idNameRow
	if rows, err := h.db.Query(`SELECT id, name FROM bots ORDER BY name`); err == nil {
		for rows.Next() {
			var b idNameRow
			rows.Scan(&b.ID, &b.Name)
			bots = append(bots, b)
		}
		rows.Close()
	}
	return hookFormData{ActiveNav: "hooks", IsEdit: isEdit, Hook: f, EventTypes: hooks.EventTypes, Bots: bots, Error: errMsg}
}

func (h *HookHandlers) NewForm(w http.ResponseWriter, r *http.Request) {
	events := map[string]bool{hooks.ReviewCompleted: true, hooks.ReviewFailed: true, hooks.ImplementationCompleted: true, hooks.ImplementationFailed: true}
	h.tpl.Render(w, "hooks/form", h.formData(hookForm{Events: events, Enabled: true}, false, ""))
}

func (h *HookHandlers) EditForm(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f, ok := h.load(id)
	if !ok {
		http.Redirect(w, r, "/hooks", http.StatusSeeOther)
		return
	}
	h.tpl.Render(w, "hooks/form", h.formData(f, true, ""))
}

func (h *HookHandlers) load(id int64) (hookForm, bool) {
	f := hookForm{ID: id, Events: map[string]bool{}}
	var types string
	var botID sql.NullInt64
	var repo, secret, headers sql.NullString
	err := h.db.QueryRow(`SELECT name, url, event_types, bot_id, repo_filter, skip_tls, enabled, secret, headers FROM hook_endpoints WHERE id = ?`, id).
		Scan(&f.Name, &f.URL, &types, &botID, &repo, &f.SkipTLS, &f.Enabled, &secret, &headers)
	if err != nil {
		return f, false
	}
	for _, t := range strings.Split(types, ",") {
		f.Events[t] = true
	}
	f.BotID, f.RepoFilter = botID.Int64, repo.String
	f.HasSecret, f.HasHeaders = secret.String != "", headers.String != ""
	return f, true
}

// Save creates or updates an endpoint. Blank secret and headers keep the stored ones.
func (h *HookHandlers) Save(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	id, _ := strconv.ParseInt(r.FormValue("id"), 10, 64)
	f := hookForm{ID: id, Name: strings.TrimSpace(r.FormValue("name")), URL: strings.TrimSpace(r.FormValue("url")),
		Events: map[string]bool{}, RepoFilter: strings.TrimSpace(r.FormValue("repoFilter")),
		SkipTLS: r.FormValue("skipTls") == "true", Enabled: r.FormValue("enabled") == "true"}
	f.BotID, _ = strconv.ParseInt(r.FormValue("botId"), 10, 64)
	var events []string
	for _, t := range hooks.EventTypes {
		if r.FormValue("event."+t) == "true" {
			f.Events[t] = true
			events = append(events, t)
		}
	}
	secret, headers := r.FormValue("secret"), strings.TrimSpace(r.FormValue("headers"))
	removeSecret := r.FormValue("removeSecret") == "true"

	if msg := validateHook(f, events, headers); msg != "" {
		if id > 0 {
			if stored, ok := h.load(id); ok {
				f.HasSecret, f.HasHeaders = stored.HasSecret, stored.HasHeaders
			}
		}
		h.tpl.Render(w, "hooks/form", h.formData(f, id > 0, msg))
		return
	}

	now := time.Now().UTC()
	botID := sql.NullInt64{Int64: f.BotID, Valid: f.BotID > 0}
	repo := sql.NullString{String: f.RepoFilter, Valid: f.RepoFilter != ""}
	if id == 0 {
		res, err := h.db.Exec(`INSERT INTO hook_endpoints (name, url, event_types, bot_id, repo_filter, skip_tls, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, f.Name, f.URL, strings.Join(events, ","), botID, repo, f.SkipTLS, f.Enabled, now, now)
		if err != nil {
			slog.Error("Failed to save hook", "err", err)
			http.Error(w, "Failed to save", http.StatusInternalServerError)
			return
		}
		id, _ = res.LastInsertId()
	} else {
		h.db.Exec(`UPDATE hook_endpoints SET name=?, url=?, event_types=?, bot_id=?, repo_filter=?, skip_tls=?, enabled=?, updated_at=? WHERE id=?`,
			f.Name, f.URL, strings.Join(events, ","), botID, repo, f.SkipTLS, f.Enabled, now, id)
	}
	switch {
	case removeSecret:
		h.db.Exec(`UPDATE hook_endpoints SET secret = NULL WHERE id = ?`, id)
	case secret != "":
		enc, _ := h.enc.Encrypt(secret)
		h.db.Exec(`UPDATE hook_endpoints SET secret = ? WHERE id = ?`, enc, id)
	}
	switch {
	case headers == "{}":
		h.db.Exec(`UPDATE hook_endpoints SET headers = NULL WHERE id = ?`, id)
	case headers != "":
		enc, _ := h.enc.Encrypt(headers)
		h.db.Exec(`UPDATE hook_endpoints SET headers = ? WHERE id = ?`, enc, id)
	}
	http.Redirect(w, r, "/hooks", http.StatusSeeOther)
}

func validateHook(f hookForm, events []string, headers string) string {
	u, err := url.Parse(f.URL)
	switch {
	case f.Name == "":
		return "Enter a name."
	case err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "":
		return "Enter an http:// or https:// URL."
	case len(events) == 0:
		return "Choose at least one event."
	}
	if f.RepoFilter != "" {
		if _, err := path.Match(f.RepoFilter, "x/y"); err != nil || !strings.Contains(f.RepoFilter, "/") {
			return `Repository filter must look like "owner/name" or "owner/*".`
		}
	}
	if headers != "" && headers != "{}" {
		var m map[string]string
		if err := json.Unmarshal([]byte(headers), &m); err != nil {
			return `Headers must be a JSON object of strings, e.g. {"Authorization": "Bearer …"}.`
		}
	}
	return ""
}

func (h *HookHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	h.db.Exec(`DELETE FROM hook_deliveries WHERE endpoint_id = ?`, id)
	h.db.Exec(`DELETE FROM hook_endpoints WHERE id = ?`, id)
	http.Redirect(w, r, "/hooks", http.StatusSeeOther)
}

// Test sends a ping to one endpoint.
func (h *HookHandlers) Test(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	h.hooks.SendTest(id)
	http.Redirect(w, r, fmt.Sprintf("/hooks/%d/deliveries", id), http.StatusSeeOther)
}

type deliveryRow struct {
	ID         int64
	EventType  string
	Status     string
	Attempts   int
	StatusCode string
	Error      string
	Payload    string
	CreatedAt  string
	NextAt     string
}

// Deliveries lists an endpoint's recent deliveries.
func (h *HookHandlers) Deliveries(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f, ok := h.load(id)
	if !ok {
		http.Redirect(w, r, "/hooks", http.StatusSeeOther)
		return
	}
	rows, err := h.db.Query(`SELECT id, event_type, status, attempts, last_status_code, COALESCE(last_error,''), payload, created_at, next_attempt_at
		FROM hook_deliveries WHERE endpoint_id = ? ORDER BY id DESC LIMIT 20`, id)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	var list []deliveryRow
	for rows.Next() {
		var d deliveryRow
		var code sql.NullInt64
		var created, next time.Time
		if rows.Scan(&d.ID, &d.EventType, &d.Status, &d.Attempts, &code, &d.Error, &d.Payload, &created, &next) != nil {
			continue
		}
		if code.Valid {
			d.StatusCode = strconv.FormatInt(code.Int64, 10)
		}
		d.CreatedAt, d.NextAt = created.Format("2006-01-02 15:04:05"), next.Format("2006-01-02 15:04:05")
		list = append(list, d)
	}
	h.tpl.Render(w, "hooks/deliveries", map[string]any{"ActiveNav": "hooks", "Hook": f, "Deliveries": list})
}

// Retry makes a failed delivery due again.
func (h *HookHandlers) Retry(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var endpoint int64
	h.db.QueryRow(`SELECT endpoint_id FROM hook_deliveries WHERE id = ?`, id).Scan(&endpoint)
	h.hooks.Retry(id)
	http.Redirect(w, r, fmt.Sprintf("/hooks/%d/deliveries", endpoint), http.StatusSeeOther)
}
