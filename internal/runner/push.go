package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrPushRejected means the remote branch has commits the workspace lacks.
var ErrPushRejected = errors.New("push rejected: the remote branch has other commits")

// Commit stages every change (respecting .gitignore) and commits it with the
// given author. It reports false when there was nothing to commit.
func (w *Workspace) Commit(ctx context.Context, message, authorName, authorEmail string) (bool, error) {
	if out, err := w.git(ctx, "add", "-A"); err != nil {
		return false, fmt.Errorf("git add: %w — %s", err, out)
	}
	if out, _ := w.git(ctx, "status", "--porcelain"); strings.TrimSpace(out) == "" {
		return false, nil
	}
	cmd := []string{"-c", "user.name=" + authorName, "-c", "user.email=" + authorEmail, "commit", "--quiet", "-m", message}
	if out, err := w.git(ctx, cmd...); err != nil {
		return false, fmt.Errorf("git commit: %w — %s", err, out)
	}
	return true, nil
}

// Push pushes HEAD to branch on origin, without force.
func (w *Workspace) Push(ctx context.Context, branch string) error {
	pushCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := w.git(pushCtx, "push", "--quiet", "origin", "HEAD:refs/heads/"+branch)
	if err == nil {
		return nil
	}
	if strings.Contains(out, "rejected") || strings.Contains(out, "non-fast-forward") || strings.Contains(out, "fetch first") {
		return fmt.Errorf("%w (%s)", ErrPushRejected, branch)
	}
	return fmt.Errorf("git push %s: %w — %s", branch, err, redactURL(out))
}

// PushNewBranch pushes HEAD to base, or to base-2 … base-5 when a branch of
// that name already holds other commits (e.g. from an earlier failed run).
// It never force-pushes. It returns the branch it pushed to.
func (w *Workspace) PushNewBranch(ctx context.Context, base string) (string, error) {
	for i := 1; i <= 5; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		err := w.Push(ctx, name)
		if err == nil {
			return name, nil
		}
		if !errors.Is(err, ErrPushRejected) {
			return "", err
		}
	}
	return "", fmt.Errorf("branches %s to %s-5 all exist with other commits", base, base)
}

// BranchSummary describes the commits HEAD has on top of base: a one-line
// log and a diff stat. It fetches what it needs; "" when it cannot tell.
func (w *Workspace) BranchSummary(ctx context.Context, base string) string {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	w.git(ctx, "fetch", "--quiet", "--deepen=50", "origin")
	if _, err := w.git(ctx, "fetch", "--quiet", "--depth=50", "origin", "refs/heads/"+base+":refs/remotes/origin/"+base); err != nil {
		return ""
	}
	log, err := w.git(ctx, "log", "--oneline", "--no-decorate", "-20", "origin/"+base+"..HEAD")
	if err != nil {
		return ""
	}
	stat, _ := w.git(ctx, "diff", "--stat", "origin/"+base+"...HEAD")
	if strings.TrimSpace(log) == "" {
		return ""
	}
	return "Commits on this branch:\n" + strings.TrimSpace(log) + "\n\nFiles changed compared with " + base + ":\n" + strings.TrimSpace(stat)
}

// StagedDiff stages every change (new files included) and returns the diff
// against HEAD.
func (w *Workspace) StagedDiff(ctx context.Context) (string, error) {
	if out, err := w.git(ctx, "add", "-A"); err != nil {
		return "", fmt.Errorf("git add: %w — %s", err, out)
	}
	out, err := w.git(ctx, "diff", "--cached")
	if err != nil {
		return "", fmt.Errorf("git diff: %w — %s", err, out)
	}
	return out, nil
}
