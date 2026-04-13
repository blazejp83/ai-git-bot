package webhook

import "encoding/json"

// ParseGitLab translates a GitLab webhook payload into a common Event.
// eventType is the value of the X-Gitlab-Event header.
func ParseGitLab(eventType string, body []byte) (*Event, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, err
	}

	ev := &Event{
		Sender: parseUser(raw, "user"),
	}

	project, _ := raw["project"].(map[string]any)
	if project != nil {
		namespace, _ := project["namespace"].(string)
		name := str(project, "name")
		ev.Repo = Repository{
			ID:       int64n(project, "id"),
			Name:     name,
			FullName: namespace + "/" + name,
			Owner:    namespace,
		}
	}

	switch eventType {
	case "Merge Request Hook":
		attrs, _ := raw["object_attributes"].(map[string]any)
		if attrs == nil {
			break
		}

		// Map GitLab actions
		action := str(attrs, "action")
		switch action {
		case "open":
			ev.Action = "opened"
		case "update":
			ev.Action = "synchronized"
		case "close", "merge":
			ev.Action = "closed"
		default:
			ev.Action = action
		}

		headSHA := ""
		if lastCommit, ok := attrs["last_commit"].(map[string]any); ok {
			headSHA = str(lastCommit, "id")
		}

		ev.PullRequest = &PullRequest{
			ID:     int64n(attrs, "id"),
			Number: int64n(attrs, "iid"),
			Title:  str(attrs, "title"),
			Body:   str(attrs, "description"),
			State:  str(attrs, "state"),
			Merged: str(attrs, "state") == "merged",
			Head:   Ref{RefName: str(attrs, "source_branch"), SHA: headSHA},
			Base:   Ref{RefName: str(attrs, "target_branch")},
		}

	case "Note Hook":
		attrs, _ := raw["object_attributes"].(map[string]any)
		if attrs == nil {
			break
		}
		ev.Action = "created"

		commentUser := ""
		if author, ok := attrs["author"].(map[string]any); ok {
			commentUser = str(author, "username")
		}

		ev.Comment = &Comment{
			ID:   int64n(attrs, "id"),
			Body: str(attrs, "note"),
			User: commentUser,
		}

		// Inline comment position
		if pos, ok := attrs["position"].(map[string]any); ok {
			ev.Comment.Path = str(pos, "new_path")
			ev.Comment.Line = int(int64n(pos, "new_line"))
		}

		noteableType := str(attrs, "noteable_type")
		if noteableType == "MergeRequest" {
			if mr, ok := raw["merge_request"].(map[string]any); ok {
				ev.PullRequest = &PullRequest{
					Number: int64n(mr, "iid"),
					Title:  str(mr, "title"),
					Body:   str(mr, "description"),
					State:  str(mr, "state"),
				}
			}
		} else if noteableType == "Issue" {
			if issue, ok := raw["issue"].(map[string]any); ok {
				ev.Issue = parseIssue(issue)
			}
		}

	case "Issue Hook":
		attrs, _ := raw["object_attributes"].(map[string]any)
		if attrs == nil {
			break
		}
		ev.Action = str(attrs, "action")
		ev.Issue = &Issue{
			Number: int64n(attrs, "iid"),
			Title:  str(attrs, "title"),
			Body:   str(attrs, "description"),
		}

		// Extract assignees
		if assignees, ok := raw["assignees"].([]any); ok {
			for _, a := range assignees {
				if am, ok := a.(map[string]any); ok {
					ev.Issue.Assignees = append(ev.Issue.Assignees, str(am, "username"))
				}
			}
			if len(ev.Issue.Assignees) > 0 {
				ev.Issue.Assignee = ev.Issue.Assignees[0]
			}
		}
	}

	return ev, nil
}
