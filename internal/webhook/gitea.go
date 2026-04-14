package webhook

import "encoding/json"

// ParseGitea translates a Gitea webhook payload into a common Event.
func ParseGitea(body []byte) (*Event, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	ev := &Event{
		Action: str(raw, "action"),
		Sender: parseUser(raw, "sender"),
		Repo:   parseRepo(raw, "repository"),
	}

	if pr, ok := raw["pull_request"].(map[string]any); ok {
		ev.PullRequest = parsePR(pr)
	}

	if issue, ok := raw["issue"].(map[string]any); ok {
		ev.Issue = parseIssue(issue)
	}

	if comment, ok := raw["comment"].(map[string]any); ok {
		ev.Comment = parseComment(comment)
	}

	if review, ok := raw["review"].(map[string]any); ok {
		ev.Review = &Review{
			ID:      int64n(review, "id"),
			Type:    str(review, "type"),
			Content: str(review, "content"),
		}
	}

	return ev, nil
}
