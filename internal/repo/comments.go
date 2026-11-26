package repo

import (
	"context"
	"time"
)

// IssueComment is one comment in an issue's (or pull request's) discussion.
type IssueComment struct {
	ID        int64
	User      string
	UserID    string
	Body      string
	CreatedAt time.Time
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func commentsFrom(raw []map[string]any, userKey, bodyKey, timeKey string) []IssueComment {
	var out []IssueComment
	for _, m := range raw {
		c := IssueComment{Body: strField(m, bodyKey), CreatedAt: parseTime(strField(m, timeKey))}
		if id, ok := m["id"].(float64); ok {
			c.ID = int64(id)
		}
		if u, ok := m[userKey].(map[string]any); ok {
			c.UserID = idString(u["id"])
			c.User = strField(u, "login")
			if c.User == "" {
				c.User = strField(u, "username")
			}
		}
		out = append(out, c)
	}
	return out
}

// ListIssueComments returns up to 100 comments, oldest first.
func (c *GiteaClient) ListIssueComments(ctx context.Context, owner, repo string, num int64) ([]IssueComment, error) {
	raw, err := httpJSON[[]map[string]any](ctx, c.http, "GET", c.apiURL("/api/v1/repos/%s/%s/issues/%d/comments?limit=100", owner, repo, num), c.headers(), nil)
	if err != nil {
		return nil, err
	}
	return commentsFrom(raw, "user", "body", "created_at"), nil
}

func (c *GitHubClient) ListIssueComments(ctx context.Context, owner, repo string, num int64) ([]IssueComment, error) {
	raw, err := httpJSON[[]map[string]any](ctx, c.http, "GET", c.apiURL("/repos/%s/%s/issues/%d/comments?per_page=100", owner, repo, num), c.headers(), nil)
	if err != nil {
		return nil, err
	}
	return commentsFrom(raw, "user", "body", "created_at"), nil
}

// ListIssueComments skips GitLab's system notes ("changed the description", ...).
func (c *GitLabClient) ListIssueComments(ctx context.Context, owner, repo string, num int64) ([]IssueComment, error) {
	raw, err := httpJSON[[]map[string]any](ctx, c.http, "GET",
		c.apiURL("/api/v4/projects/%s/issues/%d/notes?sort=asc&order_by=created_at&per_page=100", c.projectPath(owner, repo), num), c.headers(), nil)
	if err != nil {
		return nil, err
	}
	var human []map[string]any
	for _, m := range raw {
		if sys, _ := m["system"].(bool); !sys {
			human = append(human, m)
		}
	}
	return commentsFrom(human, "author", "body", "created_at"), nil
}

func (c *BitbucketClient) ListIssueComments(ctx context.Context, owner, repo string, num int64) ([]IssueComment, error) {
	page, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repositories/%s/%s/issues/%d/comments?pagelen=100", owner, repo, num), c.headers(), nil)
	if err != nil {
		return nil, err
	}
	values, _ := page["values"].([]any)
	var out []IssueComment
	for _, v := range values {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		c := IssueComment{Body: strField(m, "content", "raw"), CreatedAt: parseTime(strField(m, "created_on"))}
		if id, ok := m["id"].(float64); ok {
			c.ID = int64(id)
		}
		if u, ok := m["user"].(map[string]any); ok {
			c.UserID = strField(u, "account_id")
			c.User = strField(u, "nickname")
		}
		out = append(out, c)
	}
	return out, nil
}

// IssueDetails are an issue's title and description.
type IssueDetails struct {
	Title, Body string
}

func (c *GiteaClient) GetIssue(ctx context.Context, owner, repo string, num int64) (IssueDetails, error) {
	m, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v1/repos/%s/%s/issues/%d", owner, repo, num), c.headers(), nil)
	return IssueDetails{Title: strField(m, "title"), Body: strField(m, "body")}, err
}

func (c *GitHubClient) GetIssue(ctx context.Context, owner, repo string, num int64) (IssueDetails, error) {
	m, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repos/%s/%s/issues/%d", owner, repo, num), c.headers(), nil)
	return IssueDetails{Title: strField(m, "title"), Body: strField(m, "body")}, err
}

func (c *GitLabClient) GetIssue(ctx context.Context, owner, repo string, num int64) (IssueDetails, error) {
	m, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v4/projects/%s/issues/%d", c.projectPath(owner, repo), num), c.headers(), nil)
	return IssueDetails{Title: strField(m, "title"), Body: strField(m, "description")}, err
}

func (c *BitbucketClient) GetIssue(ctx context.Context, owner, repo string, num int64) (IssueDetails, error) {
	m, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repositories/%s/%s/issues/%d", owner, repo, num), c.headers(), nil)
	return IssueDetails{Title: strField(m, "title"), Body: strField(m, "content", "raw")}, err
}
