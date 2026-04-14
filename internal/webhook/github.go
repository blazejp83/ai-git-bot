package webhook

import "encoding/json"

// ParseGitHub translates a GitHub webhook payload into a common Event.
// eventType is the value of the X-GitHub-Event header.
func ParseGitHub(eventType string, body []byte) (*Event, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	ev := &Event{
		Action: str(raw, "action"),
		Sender: parseUser(raw, "sender"),
		Repo:   parseRepo(raw, "repository"),
	}

	switch eventType {
	case "pull_request":
		if pr, ok := raw["pull_request"].(map[string]any); ok {
			ev.PullRequest = parsePR(pr)
		}
		// Map "synchronize" -> "synchronized"
		if ev.Action == "synchronize" {
			ev.Action = "synchronized"
		}

	case "issue_comment":
		if comment, ok := raw["comment"].(map[string]any); ok {
			ev.Comment = parseComment(comment)
		}
		if issue, ok := raw["issue"].(map[string]any); ok {
			ev.Issue = parseIssue(issue)
			// If issue has a pull_request key, it's a PR comment
			if _, hasPR := issue["pull_request"]; hasPR {
				ev.Issue.IsPR = true
			}
		}

	case "pull_request_review":
		if pr, ok := raw["pull_request"].(map[string]any); ok {
			ev.PullRequest = parsePR(pr)
		}
		if review, ok := raw["review"].(map[string]any); ok {
			ev.Review = &Review{
				ID:      int64n(review, "id"),
				Type:    str(review, "state"),
				Content: str(review, "body"),
			}
		}
		if ev.Action == "submitted" {
			ev.Action = "reviewed"
		}

	case "pull_request_review_comment":
		if pr, ok := raw["pull_request"].(map[string]any); ok {
			ev.PullRequest = parsePR(pr)
		}
		if comment, ok := raw["comment"].(map[string]any); ok {
			ev.Comment = parseComment(comment)
		}

	case "issues":
		if issue, ok := raw["issue"].(map[string]any); ok {
			ev.Issue = parseIssue(issue)
		}
	}

	return ev, nil
}
