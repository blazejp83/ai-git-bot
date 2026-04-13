package repo

import "context"

// Review represents a code review on a pull request.
type Review struct {
	ID    int64
	State string
	Body  string
}

// ReviewComment represents a comment within a review.
type ReviewComment struct {
	ID   int64
	Path string
	Line int
	Body string
}

// Credentials holds the authentication details for a git provider.
type Credentials struct {
	BaseURL  string
	CloneURL string
	Username string
	Token    string
}

// TreeEntry represents a file/directory in the repository tree.
type TreeEntry struct {
	Path string
	Type string // "blob" or "tree"
}

// Client is the provider-agnostic interface for repository operations.
type Client interface {
	GetPRDiff(ctx context.Context, owner, repo string, prNum int64) (string, error)
	PostReviewComment(ctx context.Context, owner, repo string, prNum int64, body string) error
	PostComment(ctx context.Context, owner, repo string, issueNum int64, body string) error
	AddReaction(ctx context.Context, owner, repo string, commentID int64, reaction string) error
	PostInlineComment(ctx context.Context, owner, repo string, prNum int64, path string, line int, body string) error
	GetReviews(ctx context.Context, owner, repo string, prNum int64) ([]Review, error)
	GetReviewComments(ctx context.Context, owner, repo string, prNum, reviewID int64) ([]ReviewComment, error)

	GetDefaultBranch(ctx context.Context, owner, repo string) (string, error)
	GetRepoTree(ctx context.Context, owner, repo, ref string) ([]TreeEntry, error)
	GetFileContent(ctx context.Context, owner, repo, path, ref string) (string, error)
	GetFileSHA(ctx context.Context, owner, repo, path, ref string) (string, error)
	CreateBranch(ctx context.Context, owner, repo, branchName, fromRef string) error
	CreateOrUpdateFile(ctx context.Context, owner, repo, path, content, message, branch, sha string) error
	DeleteFile(ctx context.Context, owner, repo, path, message, branch, sha string) error
	CreatePR(ctx context.Context, owner, repo, title, body, head, base string) (int64, error)
	DeleteBranch(ctx context.Context, owner, repo, branchName string) error

	// FormatPRRef formats a PR/MR reference for comments (e.g. "#1" or "!1").
	FormatPRRef(prNum int64) string
}
