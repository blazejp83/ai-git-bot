package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/usage"
)

const usagePerPage = 50

// UsageHandlers serve the AI usage page.
type UsageHandlers struct {
	tpl           *Templates
	store         *usage.Store
	retentionDays int
}

func NewUsageHandlers(tpl *Templates, store *usage.Store, retentionDays int) *UsageHandlers {
	return &UsageHandlers{tpl: tpl, store: store, retentionDays: retentionDays}
}

type usagePageData struct {
	ActiveNav     string
	From, To      string // YYYY-MM-DD, as in the filter form
	Summary       usage.Summary
	Rows          []usage.Row
	Errors        []usage.ErrorRow
	Page, Pages   int
	Total         int64
	PricesAsOf    string
	RetentionDays int
	Cleared       bool
}

// List shows usage in a date range (default: the last 30 days).
func (h *UsageHandlers) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	today := time.Now().UTC().Truncate(24 * time.Hour)
	from := parseDay(q.Get("from"), today.AddDate(0, 0, -29))
	to := parseDay(q.Get("to"), today)
	if to.Before(from) {
		from, to = to, from
	}
	filter := usage.Filter{From: from, To: to.AddDate(0, 0, 1)} // "to" is inclusive

	page := 1
	if p, err := parsePositive(q.Get("page")); err == nil {
		page = p
	}

	data := usagePageData{
		ActiveNav: "usage", From: from.Format("2006-01-02"), To: to.Format("2006-01-02"),
		Page: page, PricesAsOf: usage.PricesAsOf, RetentionDays: h.retentionDays,
		Cleared: q.Get("cleared") == "1",
	}
	var err error
	if data.Summary, err = h.store.Summary(filter); err != nil {
		slog.Error("Failed to load usage summary", "err", err)
	}
	if data.Rows, data.Total, err = h.store.List(filter, page, usagePerPage); err != nil {
		slog.Error("Failed to load usage rows", "err", err)
	}
	data.Pages = int((data.Total + usagePerPage - 1) / usagePerPage)
	if data.Errors, err = h.store.Errors(filter, 20); err != nil {
		slog.Error("Failed to load AI errors", "err", err)
	}
	h.tpl.Render(w, "usage/list", data)
}

// Clear deletes all usage and error rows.
func (h *UsageHandlers) Clear(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Clear(); err != nil {
		slog.Error("Failed to clear AI usage", "err", err)
		http.Error(w, "Failed to clear usage", http.StatusInternalServerError)
		return
	}
	slog.Info("AI usage log cleared")
	http.Redirect(w, r, "/usage?cleared=1", http.StatusSeeOther)
}

func parseDay(s string, fallback time.Time) time.Time {
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t
	}
	return fallback
}

func parsePositive(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	if err == nil && n < 1 {
		n = 1
	}
	return n, err
}
