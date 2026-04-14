package server

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/tmseidel/ai-git-bot/internal/auth"
	"github.com/tmseidel/ai-git-bot/internal/bot"
	"github.com/tmseidel/ai-git-bot/internal/config"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
	"github.com/tmseidel/ai-git-bot/internal/prompt"
	"github.com/tmseidel/ai-git-bot/internal/web"
)

func New(cfg *config.Config, database *sql.DB, enc *encrypt.Service) http.Handler {
	sm := auth.NewSessionManager(cfg.SessionSecret)
	adminSvc := auth.NewAdminService(database)
	tpl := web.LoadTemplates("web/templates")
	handlers := web.NewHandlers(tpl, adminSvc, sm, database, enc)
	aiHandlers := web.NewAiHandlers(tpl, database, enc)
	oauthHandlers := web.NewOAuthHandlers(database, enc)
	botHandlers := web.NewBotHandlers(tpl, database, enc)
	gitHandlers := web.NewGitHandlers(tpl, database, enc)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// Static files
	fileServer := http.FileServer(http.Dir("web/static"))
	r.Handle("/static/*", http.StripPrefix("/static/", fileServer))
	r.Handle("/favicon.ico", http.RedirectHandler("/static/images/favicon.svg", http.StatusMovedPermanently))

	// Public routes (no auth)
	r.Get("/login", handlers.LoginPage)
	r.Post("/login", handlers.LoginSubmit)
	r.Post("/logout", handlers.Logout)
	r.Get("/setup", handlers.SetupPage)
	r.Post("/setup", handlers.SetupSubmit)

	// Health check
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	// API routes (no auth — webhook secret in URL)
	promptSvc := prompt.NewService(cfg.PromptsDir)
	webhookHandler := bot.NewWebhookHandler(database, enc, promptSvc, cfg)
	r.Route("/api", func(r chi.Router) {
		r.Post("/webhook/{secret}", webhookHandler.Handle)
	})

	// Authenticated web routes
	r.Group(func(r chi.Router) {
		r.Use(RequireAuth(sm, nil))

		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/dashboard", http.StatusSeeOther)
		})
		r.Get("/dashboard", handlers.Dashboard)

		// AI Integrations CRUD
		r.Get("/ai-integrations", aiHandlers.List)
		r.Get("/ai-integrations/new", aiHandlers.NewForm)
		r.Get("/ai-integrations/{id}/edit", aiHandlers.EditForm)
		r.Post("/ai-integrations/save", aiHandlers.Save)
		r.Post("/ai-integrations/{id}/delete", aiHandlers.Delete)

		// OAuth for AI Integrations
		r.Get("/ai-integrations/{id}/oauth", oauthHandlers.StartOAuth)
		r.Post("/ai-integrations/{id}/oauth/revoke", oauthHandlers.RevokeOAuth)

		// Bots CRUD
		r.Get("/bots", botHandlers.List)
		r.Get("/bots/new", botHandlers.NewForm)
		r.Get("/bots/{id}/edit", botHandlers.EditForm)
		r.Post("/bots/save", botHandlers.Save)
		r.Post("/bots/{id}/delete", botHandlers.Delete)

		// Git Integrations CRUD
		r.Get("/git-integrations", gitHandlers.List)
		r.Get("/git-integrations/new", gitHandlers.NewForm)
		r.Get("/git-integrations/{id}/edit", gitHandlers.EditForm)
		r.Post("/git-integrations/save", gitHandlers.Save)
		r.Post("/git-integrations/{id}/delete", gitHandlers.Delete)
	})

	slog.Info("Routes registered")
	return r
}
