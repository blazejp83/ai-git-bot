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

type BitbucketClient struct {
	creds Credentials
	http  *http.Client
}

func NewBitbucketClient(creds Credentials) *BitbucketClient {
	return &BitbucketClient{
		creds: creds,
		http:  &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return nil }},
	}
}

func (c *BitbucketClient) FormatPRRef(prNum int64) string { return fmt.Sprintf("#%d", prNum) }

func (c *BitbucketClient) authHeader() string {
	if c.creds.Username != "" {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.creds.Username+":"+c.creds.Token))
	}
	if strings.Contains(c.creds.Token, ":") {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.creds.Token))
	}
	return "Bearer " + c.creds.Token
}

func (c *BitbucketClient) headers() map[string]string {
	return map[string]string{"Authorization": c.authHeader()}
}

func (c *BitbucketClient) apiURL(path string, args ...any) string {
	return c.creds.BaseURL + fmt.Sprintf(path, args...)
}

func (c *BitbucketClient) GetPRDiff(ctx context.Context, owner, repo string, prNum int64) (string, error) {
	h := c.headers()
	h["Accept"] = "text/plain"
	return httpText(ctx, c.http, "GET", c.apiURL("/repositories/%s/%s/pullrequests/%d/diff", owner, repo, prNum), h)
}

func (c *BitbucketClient) PostReviewComment(ctx context.Context, owner, repo string, prNum int64, body string) error {
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/repositories/%s/%s/pullrequests/%d/comments", owner, repo, prNum), c.headers(),
		map[string]any{"content": map[string]string{"raw": body}})
}

func (c *BitbucketClient) PostComment(ctx context.Context, owner, repo string, issueNum int64, body string) error {
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/repositories/%s/%s/issues/%d/comments", owner, repo, issueNum), c.headers(),
		map[string]any{"content": map[string]string{"raw": body}})
}

func (c *BitbucketClient) AddReaction(ctx context.Context, owner, repo string, commentID int64, reaction string) error {
	slog.Debug("Bitbucket does not support reactions")
	return nil
}

func (c *BitbucketClient) PostInlineComment(ctx context.Context, owner, repo string, prNum int64, path string, line int, body string) error {
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/repositories/%s/%s/pullrequests/%d/comments", owner, repo, prNum), c.headers(),
		map[string]any{
			"content": map[string]string{"raw": body},
			"inline":  map[string]any{"path": path, "to": line},
		})
}

func (c *BitbucketClient) GetReviews(ctx context.Context, owner, repo string, prNum int64) ([]Review, error) {
	return nil, nil
}

func (c *BitbucketClient) GetReviewComments(ctx context.Context, owner, repo string, prNum, reviewID int64) ([]ReviewComment, error) {
	return nil, nil
}

func (c *BitbucketClient) GetDefaultBranch(ctx context.Context, owner, repo string) (string, error) {
	result, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repositories/%s/%s", owner, repo), c.headers(), nil)
	if err != nil {
		return "main", err
	}
	if mainBranch, ok := result["mainbranch"].(map[string]any); ok {
		if name, ok := mainBranch["name"].(string); ok {
			return name, nil
		}
	}
	return "main", nil
}

func (c *BitbucketClient) GetRepoTree(ctx context.Context, owner, repo, ref string) ([]TreeEntry, error) {
	result, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repositories/%s/%s/src/%s/?max_depth=10", owner, repo, ref), c.headers(), nil)
	if err != nil {
		return nil, err
	}
	values, _ := result["values"].([]any)
	var entries []TreeEntry
	for _, item := range values {
		m, _ := item.(map[string]any)
		typ := "blob"
		if fmt.Sprint(m["type"]) == "commit_directory" {
			typ = "tree"
		}
		entries = append(entries, TreeEntry{
			Path: fmt.Sprint(m["path"]),
			Type: typ,
		})
	}
	return entries, nil
}

func (c *BitbucketClient) GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error) {
	h := c.headers()
	h["Accept"] = "text/plain"
	return httpText(ctx, c.http, "GET", c.apiURL("/repositories/%s/%s/src/%s/%s", owner, repo, ref, path), h)
}

func (c *BitbucketClient) GetFileSHA(ctx context.Context, owner, repo, path, ref string) (string, error) {
	// Bitbucket doesn't expose file SHA directly; return empty to signal "create"
	return "", nil
}

func (c *BitbucketClient) resolveRef(ctx context.Context, owner, repo, ref string) (string, error) {
	result, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repositories/%s/%s/refs/branches/%s", owner, repo, ref), c.headers(), nil)
	if err != nil {
		return "", fmt.Errorf("resolve ref %s: %w", ref, err)
	}
	if target, ok := result["target"].(map[string]any); ok {
		if hash, ok := target["hash"].(string); ok {
			return hash, nil
		}
	}
	return "", fmt.Errorf("no hash found for ref %s", ref)
}

func (c *BitbucketClient) CreateBranch(ctx context.Context, owner, repo, branchName, fromRef string) error {
	hash, err := c.resolveRef(ctx, owner, repo, fromRef)
	if err != nil {
		return err
	}
	return httpNoBody(ctx, c.http, "POST", c.apiURL("/repositories/%s/%s/refs/branches", owner, repo), c.headers(),
		map[string]any{"name": branchName, "target": map[string]string{"hash": hash}})
}

func (c *BitbucketClient) CreateOrUpdateFile(ctx context.Context, owner, repo, path, content, message, branch, sha string) error {
	// Bitbucket uses form-encoded POST to /src
	data := url.Values{}
	data.Set("message", message)
	data.Set("branch", branch)
	data.Set(path, content)

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL("/repositories/%s/%s/src", owner, repo), strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", c.authHeader())

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("bitbucket file upload HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *BitbucketClient) DeleteFile(ctx context.Context, owner, repo, path, message, branch, sha string) error {
	data := url.Values{}
	data.Set("message", message)
	data.Set("branch", branch)
	data.Set("files", path)

	req, err := http.NewRequestWithContext(ctx, "POST", c.apiURL("/repositories/%s/%s/src", owner, repo), strings.NewReader(data.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Authorization", c.authHeader())

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

func (c *BitbucketClient) CreatePR(ctx context.Context, owner, repo, title, body, head, base string) (int64, error) {
	result, err := httpJSON[map[string]any](ctx, c.http, "POST", c.apiURL("/repositories/%s/%s/pullrequests", owner, repo), c.headers(),
		map[string]any{
			"title":       title,
			"description": body,
			"source":      map[string]any{"branch": map[string]string{"name": head}},
			"destination": map[string]any{"branch": map[string]string{"name": base}},
			"close_source_branch": true,
		})
	if err != nil {
		return 0, err
	}
	if id, ok := result["id"].(float64); ok {
		return int64(id), nil
	}
	return 0, fmt.Errorf("no PR id in response")
}

func (c *BitbucketClient) DeleteBranch(ctx context.Context, owner, repo, branchName string) error {
	err := httpNoBody(ctx, c.http, "DELETE", c.apiURL("/repositories/%s/%s/refs/branches/%s", owner, repo, branchName), c.headers(), nil)
	if err != nil {
		slog.Warn("Failed to delete branch", "branch", branchName, "err", err)
	}
	return nil
}
