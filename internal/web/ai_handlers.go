package web

import (
	"database/sql"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/encrypt"
)

type AiHandlers struct {
	tpl *Templates
	db  *sql.DB
	enc *encrypt.Service
}

func NewAiHandlers(tpl *Templates, db *sql.DB, enc *encrypt.Service) *AiHandlers {
	return &AiHandlers{tpl: tpl, db: db, enc: enc}
}

type aiIntegrationRow struct {
	ID           int64
	Name         string
	ProviderType string
	ApiURL       string
	Model        string
	MaxTokens    int
	CreatedAt    string
}

func (h *AiHandlers) List(w http.ResponseWriter, r *http.Request) {
	rows, err := h.db.Query(`
		SELECT id, name, provider_type, api_url, model, max_tokens, created_at
		FROM ai_integrations ORDER BY name
	`)
	if err != nil {
		slog.Error("Failed to query AI integrations", "err", err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var integrations []aiIntegrationRow
	for rows.Next() {
		var ai aiIntegrationRow
		rows.Scan(&ai.ID, &ai.Name, &ai.ProviderType, &ai.ApiURL, &ai.Model,
			&ai.MaxTokens, &ai.CreatedAt)
		integrations = append(integrations, ai)
	}

	h.tpl.Render(w, "ai-integrations/list", map[string]any{
		"ActiveNav":    "ai-integrations",
		"Integrations": integrations,
	})
}

type aiFormData struct {
	ActiveNav       string
	Integration     aiFormIntegration
	ProviderTypes   []string
	DefaultApiUrls  map[string]string
	SuggestedModels map[string][]string
	IsEdit          bool
	Error           string
}

type aiFormIntegration struct {
	ID                    int64
	Name                  string
	ProviderType          string
	ApiURL                string
	ApiVersion            string
	Model                 string
	MaxTokens             int
	MaxDiffCharsPerChunk  int
	MaxDiffChunks         int
	RetryTruncatedChunkCh int
	ExtendedThinking      bool
	ThinkingBudget        int
}

func (h *AiHandlers) NewForm(w http.ResponseWriter, r *http.Request) {
	h.tpl.Render(w, "ai-integrations/form", h.formData(aiFormIntegration{
		MaxTokens:             4096,
		MaxDiffCharsPerChunk:  120000,
		MaxDiffChunks:         8,
		RetryTruncatedChunkCh: 60000,
		ThinkingBudget:        10000,
	}, false))
}

func (h *AiHandlers) EditForm(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)

	var ai aiFormIntegration
	var apiVersion sql.NullString
	err := h.db.QueryRow(`
		SELECT id, name, provider_type, api_url, COALESCE(api_version, ''), model,
		       max_tokens, max_diff_chars_per_chunk, max_diff_chunks, retry_truncated_chunk_chars,
		       extended_thinking, thinking_budget
		FROM ai_integrations WHERE id = ?
	`, id).Scan(&ai.ID, &ai.Name, &ai.ProviderType, &ai.ApiURL, &apiVersion, &ai.Model,
		&ai.MaxTokens, &ai.MaxDiffCharsPerChunk, &ai.MaxDiffChunks, &ai.RetryTruncatedChunkCh,
		&ai.ExtendedThinking, &ai.ThinkingBudget)
	if err != nil {
		http.Redirect(w, r, "/ai-integrations", http.StatusSeeOther)
		return
	}
	if apiVersion.Valid {
		ai.ApiVersion = apiVersion.String
	}

	data := h.formData(ai, true)
	h.tpl.Render(w, "ai-integrations/form", data)
}

func (h *AiHandlers) Save(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	name := strings.TrimSpace(r.FormValue("name"))
	providerType := r.FormValue("providerType")
	apiURL := strings.TrimSpace(r.FormValue("apiUrl"))
	apiKey := r.FormValue("apiKey")
	apiVersion := r.FormValue("apiVersion")
	model := strings.TrimSpace(r.FormValue("model"))
	maxTokens, _ := strconv.Atoi(r.FormValue("maxTokens"))
	maxDiffChars, _ := strconv.Atoi(r.FormValue("maxDiffCharsPerChunk"))
	maxDiffChunks, _ := strconv.Atoi(r.FormValue("maxDiffChunks"))
	retryChars, _ := strconv.Atoi(r.FormValue("retryTruncatedChunkChars"))
	extThinking := r.FormValue("extendedThinking") == "true"
	thinkBudget, _ := strconv.Atoi(r.FormValue("thinkingBudget"))
	if thinkBudget == 0 {
		thinkBudget = 10000
	}
	idStr := r.FormValue("id")

	now := time.Now().UTC()

	if idStr != "" && idStr != "0" {
		// Update existing
		id, _ := strconv.ParseInt(idStr, 10, 64)
		if apiKey == "" {
			// Keep existing API key
			_, err := h.db.Exec(`
				UPDATE ai_integrations SET name=?, provider_type=?, api_url=?, api_version=?,
				       model=?, max_tokens=?, max_diff_chars_per_chunk=?, max_diff_chunks=?,
				       retry_truncated_chunk_chars=?, extended_thinking=?, thinking_budget=?, updated_at=?
				WHERE id=?
			`, name, providerType, apiURL, nullStr(apiVersion), model, maxTokens,
				maxDiffChars, maxDiffChunks, retryChars, extThinking, thinkBudget, now, id)
			if err != nil {
				slog.Error("Failed to update AI integration", "err", err)
			}
		} else {
			encKey, _ := h.enc.Encrypt(apiKey)
			_, err := h.db.Exec(`
				UPDATE ai_integrations SET name=?, provider_type=?, api_url=?, api_key=?, api_version=?,
				       model=?, max_tokens=?, max_diff_chars_per_chunk=?, max_diff_chunks=?,
				       retry_truncated_chunk_chars=?, extended_thinking=?, thinking_budget=?, updated_at=?
				WHERE id=?
			`, name, providerType, apiURL, encKey, nullStr(apiVersion), model, maxTokens,
				maxDiffChars, maxDiffChunks, retryChars, extThinking, thinkBudget, now, id)
			if err != nil {
				slog.Error("Failed to update AI integration", "err", err)
			}
		}
	} else {
		// Insert new
		encKey, _ := h.enc.Encrypt(apiKey)
		result, err := h.db.Exec(`
			INSERT INTO ai_integrations (name, provider_type, api_url, api_key, api_version,
			       model, max_tokens, max_diff_chars_per_chunk, max_diff_chunks,
			       retry_truncated_chunk_chars, extended_thinking, thinking_budget, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, name, providerType, apiURL, encKey, nullStr(apiVersion), model, maxTokens,
			maxDiffChars, maxDiffChunks, retryChars, extThinking, thinkBudget, now, now)
		if err != nil {
			slog.Error("Failed to insert AI integration", "err", err)
		}

		_ = result // suppress unused variable
	}

	http.Redirect(w, r, "/ai-integrations", http.StatusSeeOther)
}

func (h *AiHandlers) Delete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, _ := strconv.ParseInt(idStr, 10, 64)

	_, err := h.db.Exec("DELETE FROM ai_integrations WHERE id = ?", id)
	if err != nil {
		slog.Error("Failed to delete AI integration", "err", err)
	}

	http.Redirect(w, r, "/ai-integrations", http.StatusSeeOther)
}

func (h *AiHandlers) formData(ai aiFormIntegration, isEdit bool) *aiFormData {
	return &aiFormData{
		ActiveNav:     "ai-integrations",
		Integration:   ai,
		IsEdit:        isEdit,
		ProviderTypes: []string{"codex", "gemini", "anthropic", "ollama", "llamacpp"},
		DefaultApiUrls: map[string]string{
			"codex":     "",
			"gemini":    "",
			"anthropic": "https://api.anthropic.com",
			"ollama":    "http://localhost:11434",
			"llamacpp":  "http://localhost:8000",
		},
		SuggestedModels: map[string][]string{
			"codex":     {"gpt-5.4", "gpt-5.3-codex", "gpt-5.2-codex", "gpt-5.1-codex-mini", "gpt-5.1-codex-max"},
			"gemini":    {"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite", "gemini-3-pro-preview", "gemini-3-flash-preview"},
			"anthropic": {"claude-sonnet-4-6", "claude-opus-4-6", "claude-haiku-4-5-20251001", "claude-sonnet-4-5", "claude-opus-4-5"},
		},
	}
}

func nullStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

func jsonMap(m map[string]string) string {
	result := "{"
	first := true
	for k, v := range m {
		if !first {
			result += ","
		}
		result += fmt.Sprintf(`"%s":"%s"`, k, v)
		first = false
	}
	return result + "}"
}
