package webhook

import (
	"testing"
)

// TestFullEventParsing tests that a complex Gitea webhook payload
// with all fields populated is correctly parsed into the Event model.
func TestFullEventParsing(t *testing.T) {
	payload := `{
		"action": "opened",
		"sender": {"login": "developer"},
		"repository": {
			"id": 42,
			"name": "my-app",
			"full_name": "acme/my-app",
			"owner": {"login": "acme"}
		},
		"pull_request": {
			"id": 100,
			"number": 7,
			"title": "Add user authentication",
			"body": "This PR adds OAuth support to the application.\n\nCloses #5",
			"state": "open",
			"merged": false,
			"head": {"ref": "feature/auth", "sha": "abc123def456"},
			"base": {"ref": "main", "sha": "789xyz000111"}
		},
		"comment": {
			"id": 200,
			"body": "@reviewbot please review the auth changes",
			"user": {"login": "developer"},
			"path": "internal/auth/oauth.go",
			"line": 42,
			"pull_request_review_id": 300
		},
		"review": {
			"id": 300,
			"type": "COMMENTED",
			"content": "Looks good overall"
		}
	}`

	ev, err := ParseGitea([]byte(payload))
	if err != nil {
		t.Fatal(err)
	}

	// Verify all fields are populated
	if ev.Action != "opened" {
		t.Errorf("Action: %q", ev.Action)
	}
	if ev.Sender.Login != "developer" {
		t.Errorf("Sender: %q", ev.Sender.Login)
	}
	if ev.Repo.ID != 42 {
		t.Errorf("Repo.ID: %d", ev.Repo.ID)
	}
	if ev.Repo.Owner != "acme" {
		t.Errorf("Repo.Owner: %q", ev.Repo.Owner)
	}
	if ev.Repo.Name != "my-app" {
		t.Errorf("Repo.Name: %q", ev.Repo.Name)
	}
	if ev.Repo.FullName != "acme/my-app" {
		t.Errorf("Repo.FullName: %q", ev.Repo.FullName)
	}

	// PR
	if ev.PullRequest == nil {
		t.Fatal("PullRequest is nil")
	}
	if ev.PullRequest.Number != 7 {
		t.Errorf("PR.Number: %d", ev.PullRequest.Number)
	}
	if ev.PullRequest.Title != "Add user authentication" {
		t.Errorf("PR.Title: %q", ev.PullRequest.Title)
	}
	if ev.PullRequest.Head.RefName != "feature/auth" {
		t.Errorf("PR.Head.Ref: %q", ev.PullRequest.Head.RefName)
	}
	if ev.PullRequest.Head.SHA != "abc123def456" {
		t.Errorf("PR.Head.SHA: %q", ev.PullRequest.Head.SHA)
	}
	if ev.PullRequest.Merged {
		t.Error("PR should not be merged")
	}

	// Comment
	if ev.Comment == nil {
		t.Fatal("Comment is nil")
	}
	if ev.Comment.ID != 200 {
		t.Errorf("Comment.ID: %d", ev.Comment.ID)
	}
	if ev.Comment.Path != "internal/auth/oauth.go" {
		t.Errorf("Comment.Path: %q", ev.Comment.Path)
	}
	if ev.Comment.Line != 42 {
		t.Errorf("Comment.Line: %d", ev.Comment.Line)
	}
	if ev.Comment.PullRequestReviewID != 300 {
		t.Errorf("Comment.PullRequestReviewID: %d", ev.Comment.PullRequestReviewID)
	}

	// Review
	if ev.Review == nil {
		t.Fatal("Review is nil")
	}
	if ev.Review.Type != "COMMENTED" {
		t.Errorf("Review.Type: %q", ev.Review.Type)
	}
}

// TestGitHubMergedDetection verifies that merged PRs are correctly detected.
func TestGitHubMergedDetection(t *testing.T) {
	payload := `{
		"action": "closed",
		"sender": {"login": "user"},
		"repository": {"id": 1, "name": "r", "full_name": "o/r", "owner": {"login": "o"}},
		"pull_request": {"id": 1, "number": 1, "title": "t", "body": "", "state": "closed",
			"merged": true, "merged_at": "2024-01-01T00:00:00Z",
			"head": {"ref": "f", "sha": "s"}, "base": {"ref": "m", "sha": "s2"}}
	}`

	ev, err := ParseGitHub("pull_request", []byte(payload))
	if err != nil {
		t.Fatal(err)
	}
	if !ev.PullRequest.Merged {
		t.Fatal("PR should be detected as merged")
	}
}

// TestGitLabActionMapping verifies GitLab action name mappings.
func TestGitLabActionMapping(t *testing.T) {
	tests := []struct {
		glAction string
		want     string
	}{
		{"open", "opened"},
		{"update", "synchronized"},
		{"close", "closed"},
		{"merge", "closed"},
	}

	for _, tt := range tests {
		payload := `{
			"user": {"username": "u"},
			"project": {"id": 1, "name": "p", "namespace": "n"},
			"object_attributes": {"id": 1, "iid": 1, "title": "t", "description": "",
				"action": "` + tt.glAction + `", "state": "opened",
				"source_branch": "f", "target_branch": "m",
				"last_commit": {"id": "sha"}}
		}`

		ev, err := ParseGitLab("Merge Request Hook", []byte(payload))
		if err != nil {
			t.Fatalf("action %q: %v", tt.glAction, err)
		}
		if ev.Action != tt.want {
			t.Errorf("action %q: got %q, want %q", tt.glAction, ev.Action, tt.want)
		}
	}
}

// TestBitbucketEventKeyMapping verifies Bitbucket event key to action mapping.
func TestBitbucketEventKeyMapping(t *testing.T) {
	tests := []struct {
		eventKey string
		want     string
	}{
		{"pullrequest:created", "opened"},
		{"pullrequest:updated", "synchronized"},
		{"pullrequest:fulfilled", "closed"},
		{"pullrequest:merged", "closed"},
		{"pullrequest:declined", "closed"},
		{"pullrequest:comment_created", "created"},
	}

	minimalPayload := `{
		"actor": {"nickname": "u"},
		"pullrequest": {"id": 1, "title": "t", "description": "", "state": "OPEN",
			"source": {"branch": {"name": "f"}}, "destination": {"branch": {"name": "m"}}},
		"repository": {"name": "r", "full_name": "w/r"}
	}`

	for _, tt := range tests {
		ev, err := ParseBitbucket(tt.eventKey, []byte(minimalPayload))
		if err != nil {
			t.Fatalf("key %q: %v", tt.eventKey, err)
		}
		if ev.Action != tt.want {
			t.Errorf("key %q: got %q, want %q", tt.eventKey, ev.Action, tt.want)
		}
	}
}
