package repo

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestFileLink(t *testing.T) {
	cases := []struct {
		c    Client
		want string
	}{
		{NewGiteaClient(Credentials{BaseURL: "https://git.lan/"}), "https://git.lan/o/r/src/commit/abc/dir/my%20file.go#L10-L20"},
		{NewGitHubClient(Credentials{BaseURL: "https://api.github.com"}), "https://github.com/o/r/blob/abc/dir/my%20file.go#L10-L20"},
		{NewGitHubClient(Credentials{BaseURL: "https://ghe.corp/api/v3"}), "https://ghe.corp/o/r/blob/abc/dir/my%20file.go#L10-L20"},
		{NewGitLabClient(Credentials{BaseURL: "https://gitlab.com"}), "https://gitlab.com/o/r/-/blob/abc/dir/my%20file.go#L10-20"},
		{NewBitbucketClient(Credentials{BaseURL: "https://api.bitbucket.org/2.0"}), "https://bitbucket.org/o/r/src/abc/dir/my%20file.go#lines-10:20"},
	}
	for _, c := range cases {
		if got := c.c.FileLink("o", "r", "abc", "dir/my file.go", 10, 20); got != c.want {
			t.Errorf("got %s\nwant %s", got, c.want)
		}
	}
	g := NewGiteaClient(Credentials{BaseURL: "https://git.lan"})
	if got := g.FileLink("o", "r", "abc", "a.go", 7, 7); got != "https://git.lan/o/r/src/commit/abc/a.go#L7" {
		t.Errorf("single line: %s", got)
	}
	if got := g.FileLink("o", "r", "abc", "a.go", 0, 0); got != "https://git.lan/o/r/src/commit/abc/a.go" {
		t.Errorf("no lines: %s", got)
	}
}

type recorded struct {
	path string
	body map[string]any
}

func recordingServer(t *testing.T, fail map[string]int) (*httptest.Server, *[]recorded) {
	var mu sync.Mutex
	var reqs []recorded
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(raw, &m)
		mu.Lock()
		reqs = append(reqs, recorded{r.URL.Path, m})
		mu.Unlock()
		if code := fail[r.URL.Path]; code != 0 {
			w.WriteHeader(code)
			io.WriteString(w, `{"message":"refused"}`)
			return
		}
		io.WriteString(w, `{}`)
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func TestSubmitReview(t *testing.T) {
	// Gitea spells approval "APPROVED" and pins the commit.
	srv, reqs := recordingServer(t, nil)
	posted, err := NewGiteaClient(Credentials{BaseURL: srv.URL}).SubmitReview(context.Background(), "o", "r", 5, "body", ReviewApprove, "sha1")
	if err != nil || !posted || (*reqs)[0].body["event"] != "APPROVED" || (*reqs)[0].body["commit_id"] != "sha1" {
		t.Errorf("gitea: posted=%v err=%v %+v", posted, err, *reqs)
	}

	// Gitea refusing (e.g. approving one's own PR) is an answered error, body not posted.
	srv, _ = recordingServer(t, map[string]int{"/api/v1/repos/o/r/pulls/5/reviews": 422})
	posted, err = NewGiteaClient(Credentials{BaseURL: srv.URL}).SubmitReview(context.Background(), "o", "r", 5, "b", ReviewApprove, "s")
	if posted || !Answered(err) {
		t.Errorf("gitea refusal: posted=%v answered=%v", posted, Answered(err))
	}

	// GitLab: note posted, request-changes unsupported.
	srv, reqs = recordingServer(t, nil)
	posted, err = NewGitLabClient(Credentials{BaseURL: srv.URL}).SubmitReview(context.Background(), "o", "r", 5, "body", ReviewRequestChanges, "s")
	if !posted || !errors.Is(err, ErrDecisionUnsupported) || len(*reqs) != 1 {
		t.Errorf("gitlab request changes: posted=%v err=%v reqs=%d", posted, err, len(*reqs))
	}

	// Bitbucket: comment, then request-changes.
	srv, reqs = recordingServer(t, nil)
	posted, err = NewBitbucketClient(Credentials{BaseURL: srv.URL}).SubmitReview(context.Background(), "o", "r", 5, "body", ReviewRequestChanges, "s")
	if !posted || err != nil || len(*reqs) != 2 || (*reqs)[1].path != "/repositories/o/r/pullrequests/5/request-changes" {
		t.Errorf("bitbucket: posted=%v err=%v %+v", posted, err, *reqs)
	}

	if Answered(errors.New("connection reset")) {
		t.Error("a transport error is not an answer")
	}
}
