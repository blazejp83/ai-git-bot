package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	_ "github.com/mattn/go-sqlite3"
)

func Open(databaseURL string) (*sql.DB, error) {
	if strings.HasPrefix(databaseURL, "sqlite:") {
		// Support sqlite:///path, sqlite://path, and sqlite:/path
		path := databaseURL
		path = strings.TrimPrefix(path, "sqlite:///")
		if path == databaseURL {
			path = strings.TrimPrefix(databaseURL, "sqlite://")
		}
		// If path doesn't start with /, it's relative
		if !filepath.IsAbs(path) {
			path = filepath.Clean(path)
		}

		dir := filepath.Dir(path)
		if dir != "" && dir != "." {
			os.MkdirAll(dir, 0755) // best effort — may fail if volume-mounted
		}
		db, err := sql.Open("sqlite3", path+"?_journal_mode=WAL&_foreign_keys=on")
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}
		slog.Info("Database connected", "driver", "sqlite3", "path", path)
		return db, nil
	}

	// PostgreSQL
	db, err := sql.Open("postgres", databaseURL)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	slog.Info("Database connected", "driver", "postgres")
	return db, nil
}
