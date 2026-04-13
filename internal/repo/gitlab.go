package repo

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

type GitLabClient struct {
	creds Credentials
	http  *http.Client
}

func NewGitLabClient(creds Credentials) *GitLabClient {
	return &GitLabClient{creds: creds, http: &http.Client{}}
}

func (c *GitLabClient) FormatPRRef(prNum int64) string { return fmt.Sprintf("!%d", prNum) }

func (c *GitLabClient) headers() map[string]string {
	return map[string]string{"PRIVATE-TOKEN": c.creds.Token}
}

func (c *GitLabClient) projectPath(owner, repo string) string {
	return url.PathEscape(owner + "/" + repo)
}

func (c *GitLabClient) apiURL(path string, args ...any) string {
	return c.creds.BaseURL + fmt.Sprintf(path, args...)
}

func (c *GitLabClient) GetPRDiff(ctx context.Context, owner, repo string, prNum int64) (string, error) {
	pp := c.projectPath(owner, repo)
	// Fetch MR to get source/target branches
	mr, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v4/projects/%s/merge_requests/%d", pp, prNum), c.headers(), nil)
	if err != nil {
		return "", fmt.Errorf("fetch MR: %w", err)
	}
	source, _ := mr["source_branch"].(string)
	target, _ := mr["target_branch"].(string)

	// Compare branches to get diffs
	compare, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v4/projects/%s/repository/compare?from=%s&to=%s", pp, target, source), c.headers(), nil)
	if err != nil {
		return "", fmt.Errorf("compare branches: %w", err)
	}

	diffs, _ := compare["diffs"].([]any)
	return buildUnifiedDiff(diffs), nil
}

func buildUnifiedDiff(diffs []any) string {
	var sb strings.Builder
	for _, d := range diffs {
		m, _ := d.(map[string]any)
		oldPath, _ := m["old_path"].(string)
		newPath, _ := m["new_path"].(string)
		diff, _ := m["diff"].(string)
		fmt.Fprintf(&sb, "--- a/%s\n+++ b/%s\n%s\n", oldPath, newPath, diff)
	}
	return sb.String()
}

func (c *GitLabClient) PostReviewComment(ctx context.Context, owner, repo string, prNum int64, body string) error {
	pp := c.projectPath(owner, repo)
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/api/v4/projects/%s/merge_requests/%d/notes", pp, prNum), c.headers(),
		map[string]string{"body": body})
}

func (c *GitLabClient) PostComment(ctx context.Context, owner, repo string, issueNum int64, body string) error {
	pp := c.projectPath(owner, repo)
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/api/v4/projects/%s/issues/%d/notes", pp, issueNum), c.headers(),
		map[string]string{"body": body})
}

func (c *GitLabClient) AddReaction(ctx context.Context, owner, repo string, commentID int64, reaction string) error {
	slog.Debug("GitLab addReaction not fully supported in generic interface")
	return nil
}

func (c *GitLabClient) PostInlineComment(ctx context.Context, owner, repo string, prNum int64, path string, line int, body string) error {
	pp := c.projectPath(owner, repo)
	// Fetch MR for SHAs
	mr, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v4/projects/%s/merge_requests/%d", pp, prNum), c.headers(), nil)
	if err != nil {
		return err
	}
	diffRefs, _ := mr["diff_refs"].(map[string]any)
	baseSha, _ := diffRefs["base_sha"].(string)
	headSha, _ := diffRefs["head_sha"].(string)
	startSha, _ := diffRefs["start_sha"].(string)

	return httpNoBody(ctx, c.http, "POST", c.apiURL("/api/v4/projects/%s/merge_requests/%d/discussions", pp, prNum), c.headers(),
		map[string]any{
			"body": body,
			"position": map[string]any{
				"position_type": "text",
				"base_sha":      baseSha,
				"head_sha":      headSha,
				"start_sha":     startSha,
				"new_path":      path,
				"old_path":      path,
				"new_line":      line,
			},
		})
}

func (c *GitLabClient) GetReviews(ctx context.Context, owner, repo string, prNum int64) ([]Review, error) {
	// GitLab doesn't have a direct reviews concept; notes serve this purpose
	return nil, nil
}

func (c *GitLabClient) GetReviewComments(ctx context.Context, owner, repo string, prNum, reviewID int64) ([]ReviewComment, error) {
	return nil, nil
}

func (c *GitLabClient) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	pp := c.projectPath(owner, repo)
	info, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v4/projects/%s", pp), c.headers(), nil)
	if err != nil {
		return "main", err
	}
	if branch, ok := info["default_branch"].(string); ok {
		return branch, nil
	}
	return "main", nil
}

func (c *GitLabClient) GetRepoTree(ctx context.Context, owner, repo, ref string) ([]TreeEntry, error) {
	pp := c.projectPath(owner, repo)
	raw, err := httpJSON[[]map[string]any](ctx, c.http, "GET", c.apiURL("/api/v4/projects/%s/repository/tree?ref=%s&recursive=true&per_page=100", pp, ref), c.headers(), nil)
	if err != nil {
		return nil, err
	}
	var entries []TreeEntry
	for _, item := range raw {
		entries = append(entries, TreeEntry{
			Path: fmt.Sprint(item["path"]),
			Type: fmt.Sprint(item["type"]),
		})
	}
	return entries, nil
}

func (c *GitLabClient) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	pp := c.projectPath(owner, repo)
	encodedPath := url.PathEscape(path)
	result, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v4/projects/%s/repository/files/%s?ref=%s", pp, encodedPath, ref), c.headers(), nil)
	if err != nil {
		return "", err
	}
	if content, ok := result["content"].(string); ok {
		decoded, err := base64.StdEncoding.DecodeString(content)
		if err != nil {
			return "", err
		}
		return string(decoded), nil
	}
	return "", nil
}

func (c *GitLabClient) GetFileSHA(ctx context.Context, owner, repo, path, ref string) (string, error) {
	pp := c.projectPath(owner, repo)
	encodedPath := url.PathEscape(path)
	result, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v4/projects/%s/repository/files/%s?ref=%s", pp, encodedPath, ref), c.headers(), nil)
	if err != nil {
		return "", err
	}
	if id, ok := result["last_commit_id"].(string); ok {
		return id, nil
	}
	return "", nil
}

func (c *GitLabClient) CreateBranch(ctx context.Context, owner, repo, branchName, fromRef string) error {
	pp := c.projectPath(owner, repo)
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/api/v4/projects/%s/repository/branches", pp), c.headers(),
		map[string]string{"branch": branchName, "ref": fromRef})
}

func (c *GitLabClient) CreateOrUpdateFile(ctx context.Context, owner, repo, path, content, message, branch, sha string) error {
	pp := c.projectPath(owner, repo)
	encodedPath := url.PathEscape(path)
	b64 := base64.StdEncoding.EncodeToString([]byte(content))

	body := map[string]string{
		"content":        b64,
		"commit_message": message,
		"branch":         branch,
		"encoding":       "base64",
	}

	method := "POST"
	if sha != "" {
		method = "PUT"
		body["last_commit_id"] = sha
	}

	return httpNoBody(ctx, c.http, method, c.apiURL("/api/v4/projects/%s/repository/files/%s", pp, encodedPath), c.headers(), body)
}

func (c *GitLabClient) DeleteFile(ctx context.Context, owner, repo, path, message, branch, sha string) error {
	pp := c.projectPath(owner, repo)
	encodedPath := url.PathEscape(path)
	return httpNoBody(ctx, c.http, "DELETE", c.apiURL("/api/v4/projects/%s/repository/files/%s", pp, encodedPath), c.headers(),
		map[string]string{"commit_message": message, "branch": branch})
}

func (c *GitLabClient) CreatePR(ctx context.Context, owner, repo, title, body, head, base string) (int64, error) {
	pp := c.projectPath(owner, repo)
	result, err := httpJSON[map[string]any](ctx, c.http, "POST", c.apiURL("/api/v4/projects/%s/merge_requests", pp), c.headers(),
		map[string]string{"title": title, "description": body, "source_branch": head, "target_branch": base})
	if err != nil {
		return 0, err
	}
	if iid, ok := result["iid"].(float64); ok {
		return int64(iid), nil
	}
	return 0, fmt.Errorf("no MR iid in response")
}

func (c *GitLabClient) DeleteBranch(ctx context.Context, owner, repo, branchName string) error {
	pp := c.projectPath(owner, repo)
	err := httpNoBody(ctx, c.http, "DELETE", c.apiURL("/api/v4/projects/%s/repository/branches/%s", pp, url.PathEscape(branchName)), c.headers(), nil)
	if err != nil {
		slog.Warn("Failed to delete branch", "branch", branchName, "err", err)
	}
	return nil
}
