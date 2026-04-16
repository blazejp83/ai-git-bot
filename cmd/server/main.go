package main

import (
	"log/slog"
	"net/http"
	"net/url"
	"os"

	"github.com/tmseidel/ai-git-bot/internal/config"
	"github.com/tmseidel/ai-git-bot/internal/db"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
	"github.com/tmseidel/ai-git-bot/internal/server"
)

// maskDatabaseURL returns the database URL with any password redacted.
// Non-URL values (e.g. a plain sqlite path) are returned unchanged.
func maskDatabaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	if _, hasPw := u.User.Password(); hasPw {
		u.User = url.UserPassword(u.User.Username(), "***")
	}
	return u.String()
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg := config.Load()

	cwd, _ := os.Getwd()
	slog.Info("Starting AI Git Bot",
		"database_url", maskDatabaseURL(cfg.DatabaseURL),
		"port", cfg.Port,
		"cwd", cwd,
		"uid", os.Getuid(),
		"gid", os.Getgid(),
	)

	database, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		slog.Error("Failed to open database", "err", err)
		os.Exit(1)
	}
	defer database.Close()

	if err := db.Migrate(database, "migrations"); err != nil {
		slog.Error("Failed to run migrations", "err", err)
		os.Exit(1)
	}

	enc := encrypt.New(cfg.EncryptionKey)

	handler := server.New(cfg, database, enc)

	addr := ":" + cfg.Port
	slog.Info("Starting server", "addr", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		slog.Error("Server failed", "err", err)
		os.Exit(1)
	}
}
