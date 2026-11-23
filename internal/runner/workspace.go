package runner

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Workspace is a sandboxed git clone for the agent to operate in.
type Workspace struct {
	Dir       string
	RepoOwner string
	RepoName  string
	Branch    string
}

// NewWorkspace clones a repository into a temp directory.
func NewWorkspace(ctx context.Context, cloneURL, owner, repo, branch string) (*Workspace, error) {
	tmpDir, err := os.MkdirTemp("", "runner-workspace-")
	if err != nil {
		return nil, fmt.Errorf("create temp dir: %w", err)
	}

	slog.Info("Cloning repository for workspace", "dir", tmpDir, "branch", branch)

	cloneCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cloneCtx, "git", "clone", "--depth", "1", "--branch", branch, cloneURL, ".")
	cmd.Dir = tmpDir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, fmt.Errorf("git clone failed: %w — %s", err, stderr.String())
	}

	return &Workspace{Dir: tmpDir, RepoOwner: owner, RepoName: repo, Branch: branch}, nil
}

// CheckoutRef fetches ref from remote ("origin" or a clone URL) and checks it
// out as a detached HEAD. It returns the commit id that is now checked out.
func (w *Workspace) CheckoutRef(ctx context.Context, remote, ref string) (string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if out, err := w.git(fetchCtx, "fetch", "--depth", "50", "--no-tags", remote, ref); err != nil {
		return "", fmt.Errorf("git fetch %s: %w — %s", ref, err, redactURL(out))
	}
	if out, err := w.git(fetchCtx, "checkout", "--quiet", "--detach", "FETCH_HEAD"); err != nil {
		return "", fmt.Errorf("git checkout %s: %w — %s", ref, err, out)
	}
	sha, err := w.git(fetchCtx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse: %w — %s", err, sha)
	}
	return strings.TrimSpace(sha), nil
}

func (w *Workspace) git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = w.Dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

// redactURL hides credentials embedded in URLs (scheme://user:secret@host).
func redactURL(s string) string {
	for {
		i := strings.Index(s, "://")
		if i < 0 {
			return s
		}
		rest := s[i+3:]
		at := strings.IndexByte(rest, '@')
		end := strings.IndexAny(rest, " /\n")
		if at < 0 || (end >= 0 && end < at) {
			return s[:i+3] + redactURL(rest)
		}
		return s[:i+3] + "***@" + redactURL(rest[at+1:])
	}
}

// Resolve returns the absolute path for a relative workspace path.
// Rejects path traversal attempts.
func (w *Workspace) Resolve(path string) (string, error) {
	cleaned := filepath.Clean(path)
	if filepath.IsAbs(cleaned) {
		cleaned = strings.TrimPrefix(cleaned, "/")
	}
	abs := filepath.Join(w.Dir, cleaned)
	if !strings.HasPrefix(abs, w.Dir) {
		return "", fmt.Errorf("path traversal rejected: %s", path)
	}
	return abs, nil
}

// ReadFile reads a file from the workspace.
func (w *Workspace) ReadFile(path string) (string, error) {
	abs, err := w.Resolve(path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

// WriteFile writes content to a file in the workspace.
func (w *Workspace) WriteFile(path, content string) error {
	abs, err := w.Resolve(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0755); err != nil {
		return fmt.Errorf("mkdir for %s: %w", path, err)
	}
	return os.WriteFile(abs, []byte(content), 0644)
}

// ListFiles lists files matching a pattern in the workspace.
func (w *Workspace) ListFiles(dir, pattern string) ([]string, error) {
	searchDir := w.Dir
	if dir != "" {
		abs, err := w.Resolve(dir)
		if err != nil {
			return nil, err
		}
		searchDir = abs
	}

	var files []string
	filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		// Skip .git directory
		rel, _ := filepath.Rel(w.Dir, path)
		if strings.HasPrefix(rel, ".git/") || rel == ".git" {
			return nil
		}
		if pattern == "" || matchPattern(info.Name(), pattern) {
			files = append(files, rel)
		}
		return nil
	})
	return files, nil
}

// Shell runs a command in the workspace directory.
func (w *Workspace) Shell(ctx context.Context, command string, timeout time.Duration) (string, int, error) {
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "sh", "-c", command)
	cmd.Dir = w.Dir
	cmd.Env = append(os.Environ(), "HOME="+w.Dir, "CI=true")

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	err := cmd.Run()

	if execCtx.Err() == context.DeadlineExceeded {
		return "", -1, fmt.Errorf("command timed out after %v", timeout)
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return "", -1, fmt.Errorf("exec failed: %w", err)
		}
	}

	result := out.String()
	if len(result) > 10000 {
		result = result[:10000] + "\n... (truncated)"
	}

	return result, exitCode, nil
}

// Cleanup removes the workspace directory.
func (w *Workspace) Cleanup() {
	if w.Dir != "" {
		os.RemoveAll(w.Dir)
		slog.Debug("Cleaned up workspace", "dir", w.Dir)
	}
}

// ChangedFiles returns all files modified in the workspace (compared to git HEAD).
func (w *Workspace) ChangedFiles() ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	cmd.Dir = w.Dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Run()

	// Also include untracked files
	cmd2 := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	cmd2.Dir = w.Dir
	cmd2.Stdout = &out
	cmd2.Run()

	var files []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func matchPattern(name, pattern string) bool {
	matched, _ := filepath.Match(pattern, name)
	return matched
}
