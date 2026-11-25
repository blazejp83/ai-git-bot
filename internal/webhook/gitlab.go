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
		if src, ok := attrs["source"].(map[string]any); ok {
			ev.PullRequest.HeadRepo = str(src, "path_with_namespace")
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
		if commentUser == "" {
			commentUser = ev.Sender.Login // a Note Hook's user is the note's author
		}
		commentUserID := idStr(attrs, "author_id")
		if commentUserID == "" {
			commentUserID = ev.Sender.ID
		}

		ev.Comment = &Comment{
			ID:     int64n(attrs, "id"),
			Body:   str(attrs, "note"),
			User:   commentUser,
			UserID: commentUserID,
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

		ev.Issue.AssigneeUsers = usersFromList(raw["assignees"])
		for _, u := range ev.Issue.AssigneeUsers {
			ev.Issue.Assignees = append(ev.Issue.Assignees, u.Login)
		}
		if len(ev.Issue.Assignees) > 0 {
			ev.Issue.Assignee = ev.Issue.Assignees[0]
		}

		// GitLab has no "assigned" action: an assignment is an update whose
		// changes list the assignees before and after.
		if changes, ok := raw["changes"].(map[string]any); ok {
			if ch, ok := changes["assignees"].(map[string]any); ok {
				added := addedUsers(usersFromList(ch["previous"]), usersFromList(ch["current"]))
				if len(added) > 0 {
					ev.Action = "assigned"
					ev.Issue.AddedAssignees = added
				}
			}
		}
	}

	return ev, nil
}

// addedUsers returns the users in current that were not in previous.
func addedUsers(previous, current []User) []User {
	key := func(u User) string {
		if u.ID != "" {
			return "id:" + u.ID
		}
		return "login:" + u.Login
	}
	before := map[string]bool{}
	for _, u := range previous {
		before[key(u)] = true
	}
	var added []User
	for _, u := range current {
		if !before[key(u)] {
			added = append(added, u)
		}
	}
	return added
}
