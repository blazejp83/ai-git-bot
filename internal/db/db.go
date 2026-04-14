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
		// sqlite:///data/foo.db → /data/foo.db (absolute)
		// sqlite://data/foo.db  → data/foo.db  (relative)
		var path string
		if strings.HasPrefix(databaseURL, "sqlite:///") {
			path = "/" + strings.TrimPrefix(databaseURL, "sqlite:///")
		} else {
			path = strings.TrimPrefix(databaseURL, "sqlite://")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Clean(path)
		}

		dir := filepath.Dir(path)
		cwd, _ := os.Getwd()
		uid := os.Getuid()
		gid := os.Getgid()

		slog.Info("Database init",
			"raw_url", databaseURL,
			"resolved_path", path,
			"dir", dir,
			"cwd", cwd,
			"uid", uid,
			"gid", gid,
		)

		// Check if dir exists and is writable
		if info, err := os.Stat(dir); err != nil {
			slog.Warn("Database dir does not exist", "dir", dir, "err", err)
			if mkErr := os.MkdirAll(dir, 0755); mkErr != nil {
				slog.Error("Failed to create database dir", "dir", dir, "err", mkErr)
			}
		} else {
			slog.Info("Database dir exists", "dir", dir, "mode", info.Mode().String(), "is_dir", info.IsDir())
			// Try writing a test file to check permissions
			testFile := filepath.Join(dir, ".write-test")
			if err := os.WriteFile(testFile, []byte("ok"), 0644); err != nil {
				slog.Error("Database dir is NOT writable", "dir", dir, "err", err)
			} else {
				os.Remove(testFile)
				slog.Info("Database dir is writable", "dir", dir)
			}
		}

		dsn := path + "?_journal_mode=WAL&_foreign_keys=on"
		slog.Info("Opening SQLite", "dsn", dsn)

		db, err := sql.Open("sqlite3", dsn)
		if err != nil {
			return nil, fmt.Errorf("open sqlite: %w", err)
		}

		// sql.Open is lazy — force a connection to surface errors early
		if err := db.Ping(); err != nil {
			return nil, fmt.Errorf("sqlite ping failed: %w", err)
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
