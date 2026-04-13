package repo

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
)

type GitHubClient struct {
	creds Credentials
	http  *http.Client
}

func NewGitHubClient(creds Credentials) *GitHubClient {
	return &GitHubClient{creds: creds, http: &http.Client{}}
}

func (c *GitHubClient) FormatPRRef(prNum int64) string { return fmt.Sprintf("#%d", prNum) }

func (c *GitHubClient) headers() map[string]string {
	return map[string]string{
		"Authorization": "Bearer " + c.creds.Token,
		"Accept":        "application/vnd.github.v3+json",
	}
}

func (c *GitHubClient) apiURL(path string, args ...any) string {
	return c.creds.BaseURL + fmt.Sprintf(path, args...)
}

func (c *GitHubClient) GetPRDiff(ctx context.Context, owner, repo string, prNum int64) (string, error) {
	h := c.headers()
	h["Accept"] = "application/vnd.github.v3.diff"
	return httpText(ctx, c.http, "GET", c.apiURL("/repos/%s/%s/pulls/%d", owner, repo, prNum), h)
}

func (c *GitHubClient) PostReviewComment(ctx context.Context, owner, repo string, prNum int64, body string) error {
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/repos/%s/%s/pulls/%d/reviews", owner, repo, prNum), c.headers(),
		map[string]string{"body": body, "event": "COMMENT"})
}

func (c *GitHubClient) PostComment(ctx context.Context, owner, repo string, issueNum int64, body string) error {
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/repos/%s/%s/issues/%d/comments", owner, repo, issueNum), c.headers(),
		map[string]string{"body": body})
}

func (c *GitHubClient) AddReaction(ctx context.Context, owner, repo string, commentID int64, reaction string) error {
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/repos/%s/%s/issues/comments/%d/reactions", owner, repo, commentID), c.headers(),
		map[string]string{"content": reaction})
}

func (c *GitHubClient) PostInlineComment(ctx context.Context, owner, repo string, prNum int64, path string, line int, body string) error {
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/repos/%s/%s/pulls/%d/reviews", owner, repo, prNum), c.headers(),
		map[string]any{
			"body": "", "event": "COMMENT",
			"comments": []map[string]any{{"body": body, "path": path, "line": line}},
		})
}

func (c *GitHubClient) GetReviews(ctx context.Context, owner, repo string, prNum int64) ([]Review, error) {
	raw, err := httpJSON[[]map[string]any](ctx, c.http, "GET", c.apiURL("/repos/%s/%s/pulls/%d/reviews", owner, repo, prNum), c.headers(), nil)
	if err != nil {
		return nil, err
	}
	var reviews []Review
	for _, r := range raw {
		reviews = append(reviews, Review{
			ID:    int64(r["id"].(float64)),
			State: fmt.Sprint(r["state"]),
			Body:  fmt.Sprint(r["body"]),
		})
	}
	return reviews, nil
}

func (c *GitHubClient) GetReviewComments(ctx context.Context, owner, repo string, prNum, reviewID int64) ([]ReviewComment, error) {
	raw, err := httpJSON[[]map[string]any](ctx, c.http, "GET", c.apiURL("/repos/%s/%s/pulls/%d/reviews/%d/comments", owner, repo, prNum, reviewID), c.headers(), nil)
	if err != nil {
		return nil, err
	}
	var comments []ReviewComment
	for _, r := range raw {
		comments = append(comments, ReviewComment{
			ID:   int64(r["id"].(float64)),
			Path: fmt.Sprint(r["path"]),
			Body: fmt.Sprint(r["body"]),
		})
	}
	return comments, nil
}

func (c *GitHubClient) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	info, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repos/%s/%s", owner, repo), c.headers(), nil)
	if err != nil {
		return "main", err
	}
	if branch, ok := info["default_branch"].(string); ok {
		return branch, nil
	}
	return "main", nil
}

func (c *GitHubClient) GetRepoTree(ctx context.Context, owner, repo, ref string) ([]TreeEntry, error) {
	result, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repos/%s/%s/git/trees/%s?recursive=1", owner, repo, ref), c.headers(), nil)
	if err != nil {
		return nil, err
	}
	tree, _ := result["tree"].([]any)
	var entries []TreeEntry
	for _, item := range tree {
		m, _ := item.(map[string]any)
		entries = append(entries, TreeEntry{
			Path: fmt.Sprint(m["path"]),
			Type: fmt.Sprint(m["type"]),
		})
	}
	return entries, nil
}

func (c *GitHubClient) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	result, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref), c.headers(), nil)
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

func (c *GitHubClient) GetFileSHA(ctx context.Context, owner, repo, path, ref string) (string, error) {
	result, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, ref), c.headers(), nil)
	if err != nil {
		return "", err
	}
	if sha, ok := result["sha"].(string); ok {
		return sha, nil
	}
	return "", nil
}

// resolveRef resolves a branch name to its commit SHA.
func (c *GitHubClient) resolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	result, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repos/%s/%s/git/ref/heads/%s", owner, repo, ref), c.headers(), nil)
	if err != nil {
		return "", fmt.Errorf("resolve ref %s: %w", ref, err)
	}
	if obj, ok := result["object"].(map[string]any); ok {
		if sha, ok := obj["sha"].(string); ok {
			return sha, nil
		}
	}
	return "", fmt.Errorf("no SHA found for ref %s", ref)
}

func (c *GitHubClient) CreateBranch(ctx context.Context, owner, repo, branchName, fromRef string) error {
	sha, err := c.resolveRef(ctx, owner, repo, fromRef)
	if err != nil {
		return err
	}
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/repos/%s/%s/git/refs", owner, repo), c.headers(),
		map[string]string{"ref": "refs/heads/" + branchName, "sha": sha})
}

func (c *GitHubClient) CreateOrUpdateFile(ctx context.Context, owner, repo, path, content, message, branch, sha string) error {
	b64 := base64.StdEncoding.EncodeToString([]byte(content))
	body := map[string]string{"message": message, "content": b64, "branch": branch}
	if sha != "" {
		body["sha"] = sha
	}
	return httpNoBody(ctx, c.http, "PUT", c.apiURL("/repos/%s/%s/contents/%s", owner, repo, path), c.headers(), body)
}

func (c *GitHubClient) DeleteFile(ctx context.Context, owner, repo, path, message, branch, sha string) error {
	return httpNoBody(ctx, c.http, "DELETE", c.apiURL("/repos/%s/%s/contents/%s", owner, repo, path), c.headers(),
		map[string]string{"message": message, "branch": branch, "sha": sha})
}

func (c *GitHubClient) CreatePR(ctx context.Context, owner, repo, title, body, head, base string) (int64, error) {
	result, err := httpJSON[map[string]any](ctx, c.http, "POST", c.apiURL("/repos/%s/%s/pulls", owner, repo), c.headers(),
		map[string]string{"title": title, "body": body, "head": head, "base": base})
	if err != nil {
		return 0, err
	}
	if num, ok := result["number"].(float64); ok {
		return int64(num), nil
	}
	return 0, fmt.Errorf("no PR number in response")
}

func (c *GitHubClient) DeleteBranch(ctx context.Context, owner, repo, branchName string) error {
	err := httpNoBody(ctx, c.http, "DELETE", c.apiURL("/repos/%s/%s/git/refs/heads/%s", owner, repo, branchName), c.headers(), nil)
	if err != nil {
		slog.Warn("Failed to delete branch", "branch", branchName, "err", err)
	}
	return nil
}
