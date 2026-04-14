package main

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/tmseidel/ai-git-bot/internal/config"
	"github.com/tmseidel/ai-git-bot/internal/db"
	"github.com/tmseidel/ai-git-bot/internal/encrypt"
	"github.com/tmseidel/ai-git-bot/internal/server"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg := config.Load()

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
