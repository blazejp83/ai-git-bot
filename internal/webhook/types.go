package webhook

// Event is the common webhook event model that all platform-specific handlers translate to.
type Event struct {
	Action string // opened, synchronized, closed, created, reviewed, assigned

	Sender User
	Repo   Repository

	PullRequest *PullRequest
	Issue       *Issue
	Comment     *Comment
	Review      *Review
}

type User struct {
	Login string
}

type Repository struct {
	ID       int64
	Name     string
	FullName string
	Owner    string
}

type PullRequest struct {
	ID     int64
	Number int64
	Title  string
	Body   string
	State  string
	Merged bool
	Head   Ref
	Base   Ref
	Author string // PR author login
}

type Ref struct {
	RefName string
	SHA     string
}

type Issue struct {
	Number    int64
	Title     string
	Body      string
	IsPR      bool
	Assignee  string
	Assignees []string
}

type Comment struct {
	ID                  int64
	Body                string
	User                string
	Path                string // for inline comments
	Line                int    // for inline comments
	PullRequestReviewID int64
}

type Review struct {
	ID      int64
	Type    string // APPROVED, CHANGES_REQUESTED, COMMENTED
	Content string
}
