package webhook

import (
	"encoding/json"
	"strings"
)

// ParseBitbucket translates a Bitbucket webhook payload into a common Event.
// eventKey is the value of the X-Event-Key header.
func ParseBitbucket(eventKey string, body []byte) (*Event, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	ev := &Event{
		Sender: parseBitbucketUser(raw, "actor"),
	}

	// Map event key to action
	switch {
	case eventKey == "pullrequest:created" || eventKey == "pullrequest:open":
		ev.Action = "opened"
	case eventKey == "pullrequest:updated":
		ev.Action = "synchronized"
	case strings.HasPrefix(eventKey, "pullrequest:fulfilled") ||
		strings.HasPrefix(eventKey, "pullrequest:merged") ||
		strings.HasPrefix(eventKey, "pullrequest:rejected") ||
		strings.HasPrefix(eventKey, "pullrequest:declined"):
		ev.Action = "closed"
	case eventKey == "pullrequest:comment_created":
		ev.Action = "created"
	default:
		ev.Action = eventKey
	}

	// Parse PR
	if pr, ok := raw["pullrequest"].(map[string]any); ok {
		state := str(pr, "state")
		ev.PullRequest = &PullRequest{
			ID:     int64n(pr, "id"),
			Number: int64n(pr, "id"),
			Title:  str(pr, "title"),
			Body:   str(pr, "description"),
			State:  state,
			Merged: strings.EqualFold(state, "MERGED"),
		}

		if source, ok := pr["source"].(map[string]any); ok {
			if branch, ok := source["branch"].(map[string]any); ok {
				ev.PullRequest.Head.RefName = str(branch, "name")
			}
			if commit, ok := source["commit"].(map[string]any); ok {
				ev.PullRequest.Head.SHA = str(commit, "hash")
			}
		}
		if dest, ok := pr["destination"].(map[string]any); ok {
			if branch, ok := dest["branch"].(map[string]any); ok {
				ev.PullRequest.Base.RefName = str(branch, "name")
			}
		}
	}

	// Parse repository
	if repository, ok := raw["repository"].(map[string]any); ok {
		fullName := str(repository, "full_name")
		parts := strings.SplitN(fullName, "/", 2)
		owner := ""
		name := str(repository, "name")
		if len(parts) == 2 {
			owner = parts[0]
		}
		ev.Repo = Repository{
			Name:     name,
			FullName: fullName,
			Owner:    owner,
		}
	}

	// Parse comment
	if comment, ok := raw["comment"].(map[string]any); ok {
		body := ""
		if content, ok := comment["content"].(map[string]any); ok {
			body = str(content, "raw")
		}
		ev.Comment = &Comment{
			ID:   int64n(comment, "id"),
			Body: body,
			User: parseBitbucketUser(comment, "user").Login,
		}
		if inline, ok := comment["inline"].(map[string]any); ok {
			ev.Comment.Path = str(inline, "path")
			ev.Comment.Line = int(int64n(inline, "to"))
		}
	}

	return ev, nil
}

func parseBitbucketUser(m map[string]any, key string) User {
	actor, ok := m[key].(map[string]any)
	if !ok {
		return User{}
	}
	login := str(actor, "nickname")
	if login == "" {
		login = str(actor, "display_name")
	}
	return User{Login: login}
}
