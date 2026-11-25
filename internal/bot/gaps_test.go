package bot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/runner"
)

// fakeFiles records the file commits commitChanges makes.
type fakeFiles struct {
	repo.Client
	baseSHAs map[string]string
	failOn   string
	written  map[string]string
	deleted  []string
}

func (f *fakeFiles) GetFileSHA(_ context.Context, _, _, path, _ string) (string, error) {
	return f.baseSHAs[path], nil
}

func (f *fakeFiles) CreateOrUpdateFile(_ context.Context, _, _, path, content, _, _, _ string) error {
	if path == f.failOn {
		return errors.New("HTTP 422")
	}
	f.written[path] = content
	return nil
}

func (f *fakeFiles) DeleteFile(_ context.Context, _, _, path, _, _, sha string) error {
	if sha == "" {
		return errors.New("no sha")
	}
	f.deleted = append(f.deleted, path)
	return nil
}

func TestCommitChangesDeletesAndReportsFailures(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a"), 0o644)
	os.WriteFile(filepath.Join(dir, "bad.go"), []byte("x"), 0o644)
	ws := &runner.Workspace{Dir: dir}
	f := &fakeFiles{baseSHAs: map[string]string{"old.go": "sha-old", "a.go": "sha-a"}, failOn: "bad.go", written: map[string]string{}}

	changes := []runner.FileChange{
		{Path: "a.go"},
		{Path: "old.go", Deleted: true},
		{Path: "never-on-base.go", Deleted: true}, // created and deleted within the run
		{Path: "bad.go"},
	}
	committed, failed := commitChanges(context.Background(), f, ws, "o", "r", "main", "ai-agent/issue-1", 1, changes)

	if f.written["a.go"] != "package a" {
		t.Errorf("a.go not written: %v", f.written)
	}
	if len(f.deleted) != 1 || f.deleted[0] != "old.go" {
		t.Errorf("deleted = %v, want [old.go]", f.deleted)
	}
	if len(committed) != 2 {
		t.Errorf("committed = %+v, want a.go and old.go", committed)
	}
	if !strings.Contains(failed, "`bad.go`: HTTP 422") {
		t.Errorf("failure not reported: %q", failed)
	}
}
