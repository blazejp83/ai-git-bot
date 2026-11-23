package repo

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Account is the provider account an integration's token authenticates as.
// ID is the immutable account id (numeric id on Gitea/GitHub/GitLab,
// account_id on Bitbucket); UUID is Bitbucket's second immutable id.
type Account struct {
	ID    string
	UUID  string
	Login string
}

// PRHead describes the live head of a pull request.
type PRHead struct {
	SHA        string // full commit id of the live head ("" when unknown)
	Branch     string // source branch name
	SourceRepo string // "owner/name" of the repository holding the branch ("" when unknown)
	Ref        string // ref fetchable from the target repository (e.g. refs/pull/7/head); "" when the provider has none
	Open       bool
}

// accountCacheTTL bounds how long a resolved bot account is reused.
const accountCacheTTL = 5 * time.Minute

type accountCache struct {
	mu      sync.Mutex
	account Account
	at      time.Time
}

func (c *accountCache) get(ctx context.Context, load func(context.Context) (Account, error)) (Account, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.account.ID != "" && time.Since(c.at) < accountCacheTTL {
		return c.account, nil
	}
	a, err := load(ctx)
	if err != nil {
		return Account{}, err
	}
	if a.ID == "" {
		return Account{}, fmt.Errorf("provider returned no account id")
	}
	c.account, c.at = a, time.Now()
	return a, nil
}

func idString(v any) string {
	switch n := v.(type) {
	case float64:
		return strconv.FormatInt(int64(n), 10)
	case string:
		return n
	}
	return ""
}

func strField(m map[string]any, keys ...string) string {
	cur := m
	for i, k := range keys {
		if i == len(keys)-1 {
			s, _ := cur[k].(string)
			return s
		}
		next, ok := cur[k].(map[string]any)
		if !ok {
			return ""
		}
		cur = next
	}
	return ""
}

// --- Gitea ---

func (c *GiteaClient) CurrentUser(ctx context.Context) (Account, error) {
	return c.account.get(ctx, func(ctx context.Context) (Account, error) {
		u, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v1/user"), c.headers(), nil)
		if err != nil {
			return Account{}, err
		}
		return Account{ID: idString(u["id"]), Login: strField(u, "login")}, nil
	})
}

func (c *GiteaClient) GetPRHead(ctx context.Context, owner, repo string, prNum int64) (PRHead, error) {
	pr, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v1/repos/%s/%s/pulls/%d", owner, repo, prNum), c.headers(), nil)
	if err != nil {
		return PRHead{}, err
	}
	return PRHead{
		SHA:        strField(pr, "head", "sha"),
		Branch:     strField(pr, "head", "ref"),
		SourceRepo: strField(pr, "head", "repo", "full_name"),
		Ref:        fmt.Sprintf("refs/pull/%d/head", prNum),
		Open:       strField(pr, "state") == "open",
	}, nil
}

// --- GitHub ---

func (c *GitHubClient) CurrentUser(ctx context.Context) (Account, error) {
	return c.account.get(ctx, func(ctx context.Context) (Account, error) {
		u, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/user"), c.headers(), nil)
		if err != nil {
			return Account{}, err
		}
		return Account{ID: idString(u["id"]), Login: strField(u, "login")}, nil
	})
}

func (c *GitHubClient) GetPRHead(ctx context.Context, owner, repo string, prNum int64) (PRHead, error) {
	pr, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repos/%s/%s/pulls/%d", owner, repo, prNum), c.headers(), nil)
	if err != nil {
		return PRHead{}, err
	}
	return PRHead{
		SHA:        strField(pr, "head", "sha"),
		Branch:     strField(pr, "head", "ref"),
		SourceRepo: strField(pr, "head", "repo", "full_name"),
		Ref:        fmt.Sprintf("refs/pull/%d/head", prNum),
		Open:       strField(pr, "state") == "open",
	}, nil
}

// --- GitLab ---

func (c *GitLabClient) CurrentUser(ctx context.Context) (Account, error) {
	return c.account.get(ctx, func(ctx context.Context) (Account, error) {
		u, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v4/user"), c.headers(), nil)
		if err != nil {
			return Account{}, err
		}
		return Account{ID: idString(u["id"]), Login: strField(u, "username")}, nil
	})
}

// GetPRHead reads the merge request and, for an open one, the live tip of its
// source branch: the MR's own sha can lag behind the branch.
func (c *GitLabClient) GetPRHead(ctx context.Context, owner, repo string, prNum int64) (PRHead, error) {
	pp := c.projectPath(owner, repo)
	mr, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/api/v4/projects/%s/merge_requests/%d", pp, prNum), c.headers(), nil)
	if err != nil {
		return PRHead{}, err
	}
	head := PRHead{
		SHA:    strField(mr, "sha"),
		Branch: strField(mr, "source_branch"),
		Ref:    fmt.Sprintf("refs/merge-requests/%d/head", prNum),
		Open:   strField(mr, "state") == "opened",
	}
	srcID, tgtID := idString(mr["source_project_id"]), idString(mr["target_project_id"])
	if srcID != "" && srcID == tgtID {
		head.SourceRepo = owner + "/" + repo
	}
	if head.Open && head.Branch != "" && srcID != "" && srcID != "0" {
		br, err := httpJSON[map[string]any](ctx, c.http, "GET",
			c.apiURL("/api/v4/projects/%s/repository/branches/%s", srcID, url.PathEscape(head.Branch)), c.headers(), nil)
		if err == nil {
			if live := strField(br, "commit", "id"); live != "" {
				head.SHA = live
			}
		}
	}
	return head, nil
}

// --- Bitbucket ---

func (c *BitbucketClient) CurrentUser(ctx context.Context) (Account, error) {
	return c.account.get(ctx, func(ctx context.Context) (Account, error) {
		u, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/user"), c.headers(), nil)
		if err != nil {
			return Account{}, err
		}
		login := strField(u, "nickname")
		if login == "" {
			login = strField(u, "username")
		}
		return Account{ID: strField(u, "account_id"), UUID: strField(u, "uuid"), Login: login}, nil
	})
}

// GetPRHead reads the pull request's source side. Bitbucket reports
// abbreviated hashes, so the hash is resolved to its full id in the
// repository that owns it; an unresolvable hash is reported as unknown.
func (c *BitbucketClient) GetPRHead(ctx context.Context, owner, repo string, prNum int64) (PRHead, error) {
	pr, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repositories/%s/%s/pullrequests/%d", owner, repo, prNum), c.headers(), nil)
	if err != nil {
		return PRHead{}, err
	}
	head := PRHead{
		Branch:     strField(pr, "source", "branch", "name"),
		SourceRepo: strField(pr, "source", "repository", "full_name"),
		Open:       strings.EqualFold(strField(pr, "state"), "OPEN"),
	}
	short := strField(pr, "source", "commit", "hash")
	srcRepo := head.SourceRepo
	if srcRepo == "" {
		srcRepo = owner + "/" + repo
	}
	head.SHA = c.resolveCommit(ctx, srcRepo, short)
	return head, nil
}

func (c *BitbucketClient) resolveCommit(ctx context.Context, fullName, hash string) string {
	if hash == "" {
		return ""
	}
	if len(hash) == 40 || len(hash) == 64 {
		return hash
	}
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 {
		return ""
	}
	commit, err := httpJSON[map[string]any](ctx, c.http, "GET", c.apiURL("/repositories/%s/%s/commit/%s", parts[0], parts[1], hash), c.headers(), nil)
	if err != nil {
		return ""
	}
	full := strField(commit, "hash")
	if !strings.HasPrefix(full, hash) {
		return ""
	}
	return full
}
