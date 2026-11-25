package bot

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

// botIdentity is who the bot is on the Git provider. It is the account the
// integration's token authenticates as, matched by immutable account id; the
// configured bot username is only a fallback when the account cannot be read.
type botIdentity struct {
	account repo.Account // zero when unresolved
	login   string       // login used for mentions
	aliases []string     // mention spellings, e.g. "@bot", "@{account-id}"
}

// resolveBotIdentity reads the token's account. If that fails and a username
// is configured, the bot falls back to matching that login; with neither,
// the event must be ignored, since the bot could otherwise answer itself.
func resolveBotIdentity(ctx context.Context, client repo.Client, configuredUsername string) (botIdentity, error) {
	acct, err := client.CurrentUser(ctx)
	if err != nil {
		if configuredUsername == "" {
			return botIdentity{}, fmt.Errorf("cannot read the bot's account from its token: %w", err)
		}
		slog.Warn("Cannot read the bot's account from its token; falling back to the configured username",
			"username", configuredUsername, "err", err)
		return newBotIdentity(repo.Account{}, configuredUsername), nil
	}
	return newBotIdentity(acct, configuredUsername), nil
}

func newBotIdentity(acct repo.Account, configuredUsername string) botIdentity {
	id := botIdentity{account: acct, login: acct.Login}
	if id.login == "" {
		id.login = configuredUsername
	}
	seen := map[string]bool{}
	add := func(a string) {
		if a == "@" || a == "@{}" || seen[strings.ToLower(a)] {
			return
		}
		seen[strings.ToLower(a)] = true
		id.aliases = append(id.aliases, a)
	}
	add("@" + acct.Login)
	add("@" + configuredUsername)
	// Bitbucket stores mentions as @{account_id} or @{uuid} in the raw markup.
	add("@{" + acct.ID + "}")
	add("@{" + strings.Trim(acct.UUID, "{}") + "}")
	return id
}

// is reports whether the given user is the bot.
func (b botIdentity) is(u webhook.User) bool {
	return b.matches(u.ID, u.UUID, u.Login)
}

func (b botIdentity) matches(id, uuid, login string) bool {
	if b.account.ID != "" && id != "" {
		return id == b.account.ID
	}
	if b.account.UUID != "" && uuid != "" {
		return strings.EqualFold(strings.Trim(uuid, "{}"), strings.Trim(b.account.UUID, "{}"))
	}
	// No ids to compare: fall back to the login.
	return login != "" && b.login != "" && strings.EqualFold(login, b.login)
}

// assignedTo reports whether the bot is among the users an issue event
// assigned: the users the event added when the provider names them (GitHub,
// GitLab), otherwise the issue's current assignees (Gitea).
func (b botIdentity) assignedTo(issue *webhook.Issue) bool {
	if issue == nil {
		return false
	}
	candidates := issue.AddedAssignees
	if len(candidates) == 0 {
		candidates = issue.AssigneeUsers
	}
	for _, u := range candidates {
		if b.is(u) {
			return true
		}
	}
	return false
}

// fromBot reports whether the event was caused by the bot itself.
func (b botIdentity) fromBot(event *webhook.Event) bool {
	if b.is(event.Sender) {
		return true
	}
	if c := event.Comment; c != nil && b.matches(c.UserID, c.UserUUID, c.User) {
		return true
	}
	return false
}

// mentioned reports whether text mentions the bot. A mention must not be a
// prefix of a longer name ("@bot" does not match "@bottle").
func (b botIdentity) mentioned(text string) bool {
	lower := strings.ToLower(text)
	for _, a := range b.aliases {
		al := strings.ToLower(a)
		for i := 0; ; {
			j := strings.Index(lower[i:], al)
			if j < 0 {
				break
			}
			end := i + j + len(al)
			if end >= len(lower) || !isNameChar(lower[end]) {
				return true
			}
			i = end
		}
	}
	return false
}

// stripMentions removes the bot's mentions from text.
func (b botIdentity) stripMentions(text string) string {
	for _, a := range b.aliases {
		re := regexp.MustCompile(`(?i)` + regexp.QuoteMeta(a) + `\b`)
		if strings.HasSuffix(a, "}") {
			re = regexp.MustCompile(`(?i)` + regexp.QuoteMeta(a))
		}
		text = re.ReplaceAllString(text, " ")
	}
	return text
}

// isNameChar reports whether c can continue a (lower-cased) login. A trailing
// "." is punctuation, so "@bot." still mentions the bot.
func isNameChar(c byte) bool {
	return c == '-' || c == '_' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
}

// reviewAgainRe matches explicit requests to rerun the review: "re-review",
// "review (this / the PR / the diff ...) again", "repeat the (code) review".
// Deliberately narrow: "can you explain that review comment again?" is a
// question for the bot, not a request to review again.
var reviewAgainRe = regexp.MustCompile(`(?i)` + strings.Join([]string{
	`\bre-?\s?review\b`,
	`\breview\s+(?:(?:this|it|the|that|my|our)\s+)?(?:(?:pull[\s-]?request|pr|merge[\s-]?request|mr|changes|code|diff)\s+)?again\b`,
	`\brepeat\s+(?:(?:the|your|a)\s+)?(?:code[\s-]?)?review\b`,
}, "|"))

func isReviewAgainRequest(body string) bool {
	return reviewAgainRe.MatchString(body)
}
