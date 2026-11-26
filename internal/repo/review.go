package repo

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// ReviewEvent is the formal decision of a review.
type ReviewEvent string

const (
	ReviewApprove        ReviewEvent = "APPROVE"
	ReviewRequestChanges ReviewEvent = "REQUEST_CHANGES"
	ReviewCommentOnly    ReviewEvent = "COMMENT"
)

// ErrDecisionUnsupported means the provider has no API for the decision
// (GitLab cannot request changes); the body may still have been posted.
var ErrDecisionUnsupported = errors.New("the provider does not support this review decision")

// Answered reports whether err is a response from the provider (the request
// arrived and was refused), as opposed to an unknown outcome (timeout, reset).
func Answered(err error) bool {
	var he *HTTPError
	return errors.As(err, &he)
}

// escapePath escapes each segment of a repository path for a URL.
func escapePath(p string) string {
	parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

func lineRange(start, end int, single, multi string) string {
	if start <= 0 {
		return ""
	}
	if end <= start {
		return fmt.Sprintf(single, start)
	}
	return fmt.Sprintf(multi, start, end)
}

// webBase turns an API base URL into the web base URL.
func webBase(base string, apiSuffixes ...string) string {
	b := strings.TrimRight(base, "/")
	for _, s := range apiSuffixes {
		b = strings.TrimSuffix(b, s)
	}
	return b
}

// --- Gitea ---

func (c *GiteaClient) FileLink(owner, repo, sha, path string, start, end int) string {
	return fmt.Sprintf("%s/%s/%s/src/commit/%s/%s%s", webBase(c.creds.BaseURL, "/api/v1"), owner, repo, sha, escapePath(path),
		lineRange(start, end, "#L%d", "#L%d-L%d"))
}

func (c *GiteaClient) SubmitReview(ctx context.Context, owner, repo string, prNum int64, body string, event ReviewEvent, commitSHA string) (bool, error) {
	ev := string(event)
	if event == ReviewApprove {
		ev = "APPROVED" // Gitea's spelling
	}
	err := httpNoBody(ctx, c.http, "POST", c.apiURL("/api/v1/repos/%s/%s/pulls/%d/reviews", owner, repo, prNum), c.headers(),
		map[string]string{"body": body, "event": ev, "commit_id": commitSHA})
	return err == nil, err
}

// --- GitHub ---

func (c *GitHubClient) FileLink(owner, repo, sha, path string, start, end int) string {
	base := webBase(c.creds.BaseURL, "/api/v3")
	if strings.Contains(base, "api.github.com") {
		base = "https://github.com"
	}
	return fmt.Sprintf("%s/%s/%s/blob/%s/%s%s", base, owner, repo, sha, escapePath(path),
		lineRange(start, end, "#L%d", "#L%d-L%d"))
}

func (c *GitHubClient) SubmitReview(ctx context.Context, owner, repo string, prNum int64, body string, event ReviewEvent, commitSHA string) (bool, error) {
	err := httpNoBody(ctx, c.http, "POST", c.apiURL("/repos/%s/%s/pulls/%d/reviews", owner, repo, prNum), c.headers(),
		map[string]string{"body": body, "event": string(event), "commit_id": commitSHA})
	return err == nil, err
}

// --- GitLab ---

func (c *GitLabClient) FileLink(owner, repo, sha, path string, start, end int) string {
	return fmt.Sprintf("%s/%s/%s/-/blob/%s/%s%s", webBase(c.creds.BaseURL, "/api/v4"), owner, repo, sha, escapePath(path),
		lineRange(start, end, "#L%d", "#L%d-%d"))
}

// SubmitReview posts the body as a note, then approves. GitLab has no
// "request changes", so that decision reports ErrDecisionUnsupported after
// the note is posted.
func (c *GitLabClient) SubmitReview(ctx context.Context, owner, repo string, prNum int64, body string, event ReviewEvent, commitSHA string) (bool, error) {
	if err := c.PostReviewComment(ctx, owner, repo, prNum, body); err != nil {
		return false, err
	}
	switch event {
	case ReviewApprove:
		pp := c.projectPath(owner, repo)
		return true, httpNoBody(ctx, c.http, "POST", c.apiURL("/api/v4/projects/%s/merge_requests/%d/approve", pp, prNum), c.headers(),
			map[string]string{"sha": commitSHA})
	case ReviewRequestChanges:
		return true, ErrDecisionUnsupported
	}
	return true, nil
}

// --- Bitbucket ---

func (c *BitbucketClient) FileLink(owner, repo, sha, path string, start, end int) string {
	base := webBase(c.creds.BaseURL, "/2.0")
	if strings.Contains(base, "api.bitbucket.org") {
		base = "https://bitbucket.org"
	}
	return fmt.Sprintf("%s/%s/%s/src/%s/%s%s", base, owner, repo, sha, escapePath(path),
		lineRange(start, end, "#lines-%d", "#lines-%d:%d"))
}

// SubmitReview posts the body as a comment, then approves or requests changes.
func (c *BitbucketClient) SubmitReview(ctx context.Context, owner, repo string, prNum int64, body string, event ReviewEvent, commitSHA string) (bool, error) {
	if err := c.PostReviewComment(ctx, owner, repo, prNum, body); err != nil {
		return false, err
	}
	var action string
	switch event {
	case ReviewApprove:
		action = "approve"
	case ReviewRequestChanges:
		action = "request-changes"
	default:
		return true, nil
	}
	return true, httpNoBody(ctx, c.http, "POST", c.apiURL("/repositories/%s/%s/pullrequests/%d/%s", owner, repo, prNum, action), c.headers(), nil)
}
