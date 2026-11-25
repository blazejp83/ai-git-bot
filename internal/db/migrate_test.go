package db

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Migration 007 rebuilds runner_sessions. Existing sessions and their messages
// must survive it, and the messages' foreign key must still work afterwards.
func TestMigration007KeepsSessionsAndMessages(t *testing.T) {
	src := "../../migrations"
	stage := t.TempDir()
	copyUpTo := func(maxVersion string) {
		entries, err := os.ReadDir(src)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".up.sql") || e.Name()[:3] > maxVersion {
				continue
			}
			data, _ := os.ReadFile(filepath.Join(src, e.Name()))
			os.WriteFile(filepath.Join(stage, e.Name()), data, 0o644)
		}
	}

	conn, err := Open("sqlite://" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	copyUpTo("006")
	if err := Migrate(conn, stage); err != nil {
		t.Fatal(err)
	}
	mustExec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	mustExec(`INSERT INTO runner_sessions (id, mode, repo_owner, repo_name, target_number, target_type, status)
		VALUES (5, 'review', 'o', 'r', 7, 'pr', 'COMPLETED')`)
	mustExec(`INSERT INTO runner_messages (runner_session_id, role, message_type, content) VALUES (5, 'user', 'text', 'hello')`)

	copyUpTo("007")
	if err := Migrate(conn, stage); err != nil {
		t.Fatal(err)
	}

	var n int
	conn.QueryRow(`SELECT COUNT(*) FROM runner_messages WHERE runner_session_id = 5`).Scan(&n)
	if n != 1 {
		t.Fatalf("messages after migration = %d, want 1", n)
	}
	rows, err := conn.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		t.Error("foreign_key_check reports violations after migration")
	}
	rows.Close()

	// A second session for the same target is allowed now.
	mustExec(`INSERT INTO runner_sessions (mode, repo_owner, repo_name, target_number, target_type) VALUES ('review', 'o', 'r', 7, 'pr')`)
	// The messages' foreign key still points at runner_sessions and cascades.
	mustExec(`DELETE FROM runner_sessions WHERE id = 5`)
	conn.QueryRow(`SELECT COUNT(*) FROM runner_messages`).Scan(&n)
	if n != 0 {
		t.Errorf("messages not cascaded on delete: %d left", n)
	}
	if _, err := conn.Exec(`INSERT INTO runner_messages (runner_session_id, role, message_type) VALUES (999, 'user', 'text')`); err == nil {
		t.Error("foreign key to runner_sessions no longer enforced")
	}
}
