package db

import (
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func Migrate(db *sql.DB, migrationsDir string) error {
	// Create migrations tracking table
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return fmt.Errorf("create migrations table: %w", err)
	}

	entries, err := os.ReadDir(migrationsDir)
	if err != nil {
		return fmt.Errorf("read migrations dir: %w", err)
	}

	var upFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".up.sql") {
			upFiles = append(upFiles, e.Name())
		}
	}
	sort.Strings(upFiles)

	for _, fname := range upFiles {
		version := strings.TrimSuffix(fname, ".up.sql")

		var count int
		err := db.QueryRow("SELECT COUNT(*) FROM schema_migrations WHERE version = ?", version).Scan(&count)
		if err != nil {
			return fmt.Errorf("check migration %s: %w", version, err)
		}
		if count > 0 {
			continue
		}

		sqlBytes, err := os.ReadFile(filepath.Join(migrationsDir, fname))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", fname, err)
		}

		slog.Info("Applying migration", "version", version)
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", version, err)
		}

		if _, err := db.Exec("INSERT INTO schema_migrations (version) VALUES (?)", version); err != nil {
			return fmt.Errorf("record migration %s: %w", version, err)
		}
	}

	return nil
}
