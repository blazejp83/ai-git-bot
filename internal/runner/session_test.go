package runner

import (
	"path/filepath"
	"testing"

	"github.com/tmseidel/ai-git-bot/internal/db"
)

func openTestDB(t *testing.T) *Session {
	t.Helper()
	conn, err := db.Open("sqlite://" + filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(conn, "../../migrations"); err != nil {
		t.Fatal(err)
	}
	return &Session{db: conn}
}

// A PR is reviewed again after every push, and an issue can be worked on more
// than once: each run needs its own session for the same target.
func TestSeveralSessionsPerTarget(t *testing.T) {
	s := openTestDB(t)
	first, err := CreateSession(s.db, ModeReview, "o", "r", 7, "pr", "p")
	if err != nil {
		t.Fatal(err)
	}
	first.SaveMessage("user", "text", "first review", "", "", "")
	second, err := CreateSession(s.db, ModeReview, "o", "r", 7, "pr", "p")
	if err != nil {
		t.Fatalf("second session for the same PR: %v", err)
	}
	if second.SessionID == first.SessionID {
		t.Fatal("expected a new session")
	}
	if msgs := first.LoadMessages(); len(msgs) != 1 || msgs[0].Content != "first review" {
		t.Errorf("first session's messages lost: %+v", msgs)
	}
	latest, err := LoadSession(s.db, "o", "r", "pr", 7)
	if err != nil || latest.SessionID != second.SessionID {
		t.Errorf("LoadSession = %v, %v; want the newest session %d", latest, err, second.SessionID)
	}
}
