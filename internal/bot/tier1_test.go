package bot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

func TestBotIdentityMatchesByAccountID(t *testing.T) {
	id := newBotIdentity(repo.Account{ID: "42", Login: "ai-bot"}, "old-name")

	if !id.is(webhook.User{ID: "42", Login: "renamed"}) {
		t.Error("same id with a changed login must be the bot")
	}
	if id.is(webhook.User{ID: "7", Login: "ai-bot"}) {
		t.Error("the bot's login with another id must not be the bot")
	}
	if !id.is(webhook.User{Login: "AI-Bot"}) {
		t.Error("without ids, the login decides")
	}
	ev := &webhook.Event{Sender: webhook.User{ID: "1"}, Comment: &webhook.Comment{UserID: "42"}}
	if !id.fromBot(ev) {
		t.Error("a comment by the bot must count as from the bot")
	}
}

func TestBotIdentityBitbucketUUID(t *testing.T) {
	id := newBotIdentity(repo.Account{ID: "557058:abc", UUID: "{u-1}", Login: "bb-bot"}, "")
	if !id.is(webhook.User{UUID: "u-1"}) {
		t.Error("uuid with or without braces must match")
	}
	if !id.mentioned("hey @{557058:abc} please look") {
		t.Error("Bitbucket @{account_id} markup must count as a mention")
	}
}

func TestBotIdentityMentions(t *testing.T) {
	id := newBotIdentity(repo.Account{ID: "1", Login: "bot"}, "helper")
	cases := map[string]bool{
		"@bot can you check":  true,
		"thanks @BOT.":        true,
		"ping @helper":        true, // configured username still works
		"talk to @bottle":     false,
		"email me@bot-x.com":  false,
		"no mention here bot": false,
	}
	for text, want := range cases {
		if got := id.mentioned(text); got != want {
			t.Errorf("mentioned(%q) = %v, want %v", text, got, want)
		}
	}
	if got := id.stripMentions("@bot re-review please"); got != "  re-review please" {
		t.Errorf("stripMentions = %q", got)
	}
}

type failingUserClient struct{ repo.Client }

func (failingUserClient) CurrentUser(context.Context) (repo.Account, error) {
	return repo.Account{}, errors.New("401")
}

func TestResolveBotIdentityFallback(t *testing.T) {
	if _, err := resolveBotIdentity(context.Background(), failingUserClient{}, ""); err == nil {
		t.Error("no account and no username must fail closed")
	}
	id, err := resolveBotIdentity(context.Background(), failingUserClient{}, "cfg-bot")
	if err != nil || !id.is(webhook.User{Login: "cfg-bot"}) {
		t.Errorf("username fallback failed: %v", err)
	}
}

func TestReviewAgainRequest(t *testing.T) {
	yes := []string{"re-review please", "please rereview", "can you review this PR again?", "review the merge request again",
		"Review again", "repeat the code review", "re review"}
	no := []string{"can you explain that review comment again?", "the review flagged this again, why?",
		"I reviewed it", "what does this review mean"}
	for _, s := range yes {
		if !isReviewAgainRequest(s) {
			t.Errorf("%q should be a review-again request", s)
		}
	}
	for _, s := range no {
		if isReviewAgainRequest(s) {
			t.Errorf("%q should not be a review-again request", s)
		}
	}
}

func TestReviewDebouncer(t *testing.T) {
	now := time.Unix(1000, 0)
	d := newReviewDebouncer()
	d.now = func() time.Time { return now }
	key := debounceKey(1, "o/r", 5)

	if !d.tryClaim(key, "abc") {
		t.Fatal("first trigger must run")
	}
	if d.tryClaim(key, "abc") {
		t.Error("second trigger for the same commit must be dropped")
	}
	if !d.tryClaim(key, "def") {
		t.Error("a new commit must run")
	}
	d.release(key, "abc") // stale release must not drop the newer claim
	if d.tryClaim(key, "def") {
		t.Error("release of an older sha must keep the current claim")
	}
	d.release(key, "def")
	if !d.tryClaim(key, "def") {
		t.Error("after release the commit must run again")
	}
	now = now.Add(61 * time.Second)
	if !d.tryClaim(key, "def") {
		t.Error("after the window the commit must run again")
	}
	d.forget(key)
	if !d.tryClaim(key, "def") {
		t.Error("after close/forget the commit must run again")
	}
	if !d.tryClaim(key, "") || !d.tryClaim(key, "") {
		t.Error("a trigger without sha is never debounced")
	}

	d.max = 2
	d.tryClaim("k1", "a")
	d.tryClaim("k2", "a")
	d.tryClaim("k3", "a")
	if len(d.entries) > 2 {
		t.Errorf("entries = %d, want <= 2", len(d.entries))
	}
}

func TestReviewsTargetBranch(t *testing.T) {
	all := botSettings{}
	if !all.reviewsTargetBranch("develop") {
		t.Error("no filter must allow every branch")
	}
	bs := botSettings{prTargetBranches: parseAllowlist("master, release/*")}
	for base, want := range map[string]bool{"master": true, "release/1.2": true, "develop": false, "feature/master": false, "": false} {
		if got := bs.reviewsTargetBranch(base); got != want {
			t.Errorf("reviewsTargetBranch(%q) = %v, want %v", base, got, want)
		}
	}
}
