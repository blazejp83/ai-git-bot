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
	ID    string // immutable account id (numeric id; account_id on Bitbucket)
	UUID  string // Bitbucket only
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

	HeadRepo string // "owner/name" of the repository holding the head branch ("" when unknown)
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
	UserID              string // immutable account id of the comment author
	UserUUID            string // Bitbucket only
	Path                string // for inline comments
	Line                int    // for inline comments
	PullRequestReviewID int64
}

type Review struct {
	ID      int64
	Type    string // APPROVED, CHANGES_REQUESTED, COMMENTED
	Content string
}
