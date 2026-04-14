package webhook

import (
	"testing"
)

func TestParseGitea_PullRequest(t *testing.T) {
	payload := `{
		"action": "opened",
		"sender": {"login": "user1"},
		"repository": {"id": 1, "name": "repo", "full_name": "owner/repo", "owner": {"login": "owner"}},
		"pull_request": {"id": 10, "number": 5, "title": "Fix bug", "body": "Fixes #1", "state": "open", "merged": false,
			"head": {"ref": "feature", "sha": "abc123"},
			"base": {"ref": "main", "sha": "def456"}}
	}`

	ev, err := ParseGitea([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Action != "opened" {
		t.Fatalf("Action: got %q", ev.Action)
	}
	if ev.Sender.Login != "user1" {
		t.Fatalf("Sender: got %q", ev.Sender.Login)
	}
	if ev.Repo.Owner != "owner" || ev.Repo.Name != "repo" {
		t.Fatalf("Repo: got %q/%q", ev.Repo.Owner, ev.Repo.Name)
	}
	if ev.PullRequest == nil {
		t.Fatal("PR should not be nil")
	}
	if ev.PullRequest.Number != 5 {
		t.Fatalf("PR number: got %d", ev.PullRequest.Number)
	}
	if ev.PullRequest.Head.RefName != "feature" || ev.PullRequest.Head.SHA != "abc123" {
		t.Fatalf("Head: got %q/%q", ev.PullRequest.Head.RefName, ev.PullRequest.Head.SHA)
	}
}

func TestParseGitea_Comment(t *testing.T) {
	payload := `{
		"action": "created",
		"sender": {"login": "user1"},
		"repository": {"id": 1, "name": "repo", "full_name": "owner/repo", "owner": {"login": "owner"}},
		"comment": {"id": 42, "body": "@bot please review", "user": {"login": "commenter"}},
		"issue": {"number": 5, "title": "Issue title", "body": "Description"}
	}`

	ev, err := ParseGitea([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Comment == nil {
		t.Fatal("Comment should not be nil")
	}
	if ev.Comment.ID != 42 || ev.Comment.Body != "@bot please review" {
		t.Fatalf("Comment: got id=%d body=%q", ev.Comment.ID, ev.Comment.Body)
	}
	if ev.Comment.User != "commenter" {
		t.Fatalf("Comment user: got %q", ev.Comment.User)
	}
	if ev.Issue == nil || ev.Issue.Number != 5 {
		t.Fatal("Issue should be parsed")
	}
}

func TestParseGitHub_PullRequest(t *testing.T) {
	payload := `{
		"action": "synchronize",
		"sender": {"login": "ghuser"},
		"repository": {"id": 100, "name": "myrepo", "full_name": "org/myrepo", "owner": {"login": "org"}},
		"pull_request": {"id": 50, "number": 12, "title": "Update deps", "body": "", "state": "open",
			"head": {"ref": "update-deps", "sha": "sha1"},
			"base": {"ref": "main", "sha": "sha2"}}
	}`

	ev, err := ParseGitHub("pull_request", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Action != "synchronized" {
		t.Fatalf("Action should be mapped from synchronize to synchronized, got %q", ev.Action)
	}
	if ev.PullRequest == nil || ev.PullRequest.Number != 12 {
		t.Fatal("PR should be parsed")
	}
}

func TestParseGitHub_IssueComment(t *testing.T) {
	payload := `{
		"action": "created",
		"sender": {"login": "ghuser"},
		"repository": {"id": 100, "name": "myrepo", "full_name": "org/myrepo", "owner": {"login": "org"}},
		"comment": {"id": 99, "body": "@bot help", "user": {"login": "commenter"}},
		"issue": {"number": 7, "title": "Bug", "body": "desc", "pull_request": {"url": "..."}}
	}`

	ev, err := ParseGitHub("issue_comment", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Comment == nil || ev.Comment.Body != "@bot help" {
		t.Fatal("Comment should be parsed")
	}
	if ev.Issue == nil || !ev.Issue.IsPR {
		t.Fatal("Issue should be marked as PR (has pull_request key)")
	}
}

func TestParseGitHub_Review(t *testing.T) {
	payload := `{
		"action": "submitted",
		"sender": {"login": "reviewer"},
		"repository": {"id": 100, "name": "repo", "full_name": "org/repo", "owner": {"login": "org"}},
		"pull_request": {"id": 1, "number": 3, "title": "PR", "body": "", "state": "open",
			"head": {"ref": "f", "sha": "s"}, "base": {"ref": "m", "sha": "s2"}},
		"review": {"id": 77, "state": "commented", "body": "LGTM"}
	}`

	ev, err := ParseGitHub("pull_request_review", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Action != "reviewed" {
		t.Fatalf("Action should be mapped to 'reviewed', got %q", ev.Action)
	}
	if ev.Review == nil || ev.Review.ID != 77 {
		t.Fatal("Review should be parsed")
	}
}

func TestParseGitLab_MergeRequest(t *testing.T) {
	payload := `{
		"user": {"username": "gluser"},
		"project": {"id": 10, "name": "proj", "namespace": "mygroup"},
		"object_attributes": {
			"id": 200, "iid": 15, "title": "MR title", "description": "MR desc",
			"action": "open", "state": "opened",
			"source_branch": "feature-x", "target_branch": "main",
			"last_commit": {"id": "commitsha"}
		}
	}`

	ev, err := ParseGitLab("Merge Request Hook", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Action != "opened" {
		t.Fatalf("Action 'open' should map to 'opened', got %q", ev.Action)
	}
	if ev.PullRequest == nil {
		t.Fatal("PR should be parsed from MR")
	}
	if ev.PullRequest.Number != 15 {
		t.Fatalf("MR iid should be Number, got %d", ev.PullRequest.Number)
	}
	if ev.PullRequest.Head.RefName != "feature-x" {
		t.Fatalf("Head ref: got %q", ev.PullRequest.Head.RefName)
	}
	if ev.PullRequest.Head.SHA != "commitsha" {
		t.Fatalf("Head SHA: got %q", ev.PullRequest.Head.SHA)
	}
	if ev.Repo.Owner != "mygroup" || ev.Repo.Name != "proj" {
		t.Fatalf("Repo: got %q/%q", ev.Repo.Owner, ev.Repo.Name)
	}
}

func TestParseGitLab_Note(t *testing.T) {
	payload := `{
		"user": {"username": "noter"},
		"project": {"id": 10, "name": "proj", "namespace": "group"},
		"object_attributes": {
			"id": 300, "note": "Please fix this", "noteable_type": "MergeRequest",
			"author": {"username": "noter"}
		},
		"merge_request": {"iid": 8, "title": "MR", "description": "desc", "state": "opened"}
	}`

	ev, err := ParseGitLab("Note Hook", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Action != "created" {
		t.Fatalf("Note hook should set action to 'created', got %q", ev.Action)
	}
	if ev.Comment == nil || ev.Comment.Body != "Please fix this" {
		t.Fatal("Comment should be parsed from note")
	}
	if ev.PullRequest == nil || ev.PullRequest.Number != 8 {
		t.Fatal("MR should be parsed for MR notes")
	}
}

func TestParseBitbucket_PRCreated(t *testing.T) {
	payload := `{
		"actor": {"nickname": "bbuser"},
		"pullrequest": {
			"id": 42, "title": "New feature", "description": "Adds stuff", "state": "OPEN",
			"source": {"branch": {"name": "feature"}, "commit": {"hash": "abc"}},
			"destination": {"branch": {"name": "main"}}
		},
		"repository": {"name": "myrepo", "full_name": "workspace/myrepo"}
	}`

	ev, err := ParseBitbucket("pullrequest:created", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Action != "opened" {
		t.Fatalf("pullrequest:created should map to 'opened', got %q", ev.Action)
	}
	if ev.Sender.Login != "bbuser" {
		t.Fatalf("Sender: got %q", ev.Sender.Login)
	}
	if ev.PullRequest == nil || ev.PullRequest.ID != 42 {
		t.Fatal("PR should be parsed")
	}
	if ev.PullRequest.Head.RefName != "feature" {
		t.Fatalf("Head ref: got %q", ev.PullRequest.Head.RefName)
	}
	if ev.Repo.Owner != "workspace" {
		t.Fatalf("Owner should be parsed from full_name, got %q", ev.Repo.Owner)
	}
}

func TestParseBitbucket_Comment(t *testing.T) {
	payload := `{
		"actor": {"nickname": "bbuser"},
		"pullrequest": {"id": 1, "title": "PR", "description": "", "state": "OPEN",
			"source": {"branch": {"name": "f"}}, "destination": {"branch": {"name": "m"}}},
		"repository": {"name": "repo", "full_name": "ws/repo"},
		"comment": {
			"id": 55, "content": {"raw": "@bot review this"},
			"user": {"nickname": "commenter"},
			"inline": {"path": "src/main.go", "to": 42}
		}
	}`

	ev, err := ParseBitbucket("pullrequest:comment_created", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if ev.Action != "created" {
		t.Fatalf("Action: got %q", ev.Action)
	}
	if ev.Comment == nil {
		t.Fatal("Comment should be parsed")
	}
	if ev.Comment.Body != "@bot review this" {
		t.Fatalf("Comment body: got %q", ev.Comment.Body)
	}
	if ev.Comment.Path != "src/main.go" || ev.Comment.Line != 42 {
		t.Fatalf("Inline: got path=%q line=%d", ev.Comment.Path, ev.Comment.Line)
	}
}

func TestHelpers(t *testing.T) {
	m := map[string]any{"name": "test", "count": float64(42)}
	if str(m, "name") != "test" {
		t.Fatal("str failed")
	}
	if str(m, "missing") != "" {
		t.Fatal("str missing should return empty")
	}
	if int64n(m, "count") != 42 {
		t.Fatal("int64n failed")
	}
	if int64n(m, "missing") != 0 {
		t.Fatal("int64n missing should return 0")
	}
}
