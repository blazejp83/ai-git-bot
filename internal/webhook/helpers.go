package webhook

import "strconv"

// str extracts a string from a JSON map.
func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// idStr extracts an id that may be a JSON number or string.
func idStr(m map[string]any, key string) string {
	switch v := m[key].(type) {
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case string:
		return v
	}
	return ""
}

// int64n extracts a number as int64 from a JSON map.
func int64n(m map[string]any, key string) int64 {
	if v, ok := m[key].(float64); ok {
		return int64(v)
	}
	return 0
}

// parseUser extracts a user from a JSON map at the given key.
func parseUser(m map[string]any, key string) User {
	user, ok := m[key].(map[string]any)
	if !ok {
		return User{}
	}
	login := str(user, "login")
	if login == "" {
		login = str(user, "username")
	}
	return User{ID: idStr(user, "id"), Login: login}
}

// userFromMap reads a user object (login or username, numeric or string id).
func userFromMap(u map[string]any) User {
	login := str(u, "login")
	if login == "" {
		login = str(u, "username")
	}
	return User{ID: idStr(u, "id"), Login: login}
}

// usersFromList reads a JSON array of user objects.
func usersFromList(v any) []User {
	list, _ := v.([]any)
	var users []User
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			if u := userFromMap(m); u.Login != "" || u.ID != "" {
				users = append(users, u)
			}
		}
	}
	return users
}

// parseRepo extracts a repository from a JSON map at the given key.
func parseRepo(m map[string]any, key string) Repository {
	repo, ok := m[key].(map[string]any)
	if !ok {
		return Repository{}
	}
	owner := ""
	if ownerMap, ok := repo["owner"].(map[string]any); ok {
		owner = str(ownerMap, "login")
		if owner == "" {
			owner = str(ownerMap, "username")
		}
	}
	return Repository{
		ID:       int64n(repo, "id"),
		Name:     str(repo, "name"),
		FullName: str(repo, "full_name"),
		Owner:    owner,
	}
}

// parsePR extracts a pull request from a JSON map.
func parsePR(pr map[string]any) *PullRequest {
	merged := false
	if v, ok := pr["merged"].(bool); ok {
		merged = v
	}
	if str(pr, "merged_at") != "" {
		merged = true
	}

	result := &PullRequest{
		ID:     int64n(pr, "id"),
		Number: int64n(pr, "number"),
		Title:  str(pr, "title"),
		Body:   str(pr, "body"),
		State:  str(pr, "state"),
		Merged: merged,
	}

	// Extract PR author from user/author object
	if user, ok := pr["user"].(map[string]any); ok {
		result.Author = str(user, "login")
		if result.Author == "" {
			result.Author = str(user, "username")
		}
	}
	if result.Author == "" {
		if author, ok := pr["author"].(map[string]any); ok {
			result.Author = str(author, "login")
			if result.Author == "" {
				result.Author = str(author, "username")
			}
		}
	}

	if head, ok := pr["head"].(map[string]any); ok {
		result.Head = Ref{RefName: str(head, "ref"), SHA: str(head, "sha")}
		if repo, ok := head["repo"].(map[string]any); ok {
			result.HeadRepo = str(repo, "full_name")
		}
	}
	if base, ok := pr["base"].(map[string]any); ok {
		result.Base = Ref{RefName: str(base, "ref"), SHA: str(base, "sha")}
	}

	return result
}

// parseIssue extracts an issue from a JSON map.
func parseIssue(issue map[string]any) *Issue {
	result := &Issue{
		Number: int64n(issue, "number"),
		Title:  str(issue, "title"),
		Body:   str(issue, "body"),
	}
	if result.Number == 0 {
		result.Number = int64n(issue, "iid")
	}

	result.AssigneeUsers = usersFromList(issue["assignees"])
	if assignee, ok := issue["assignee"].(map[string]any); ok {
		u := userFromMap(assignee)
		result.Assignee = u.Login
		if len(result.AssigneeUsers) == 0 && (u.Login != "" || u.ID != "") {
			result.AssigneeUsers = []User{u}
		}
	}
	for _, u := range result.AssigneeUsers {
		result.Assignees = append(result.Assignees, u.Login)
	}
	_, result.IsPR = issue["pull_request"]

	return result
}

// parseComment extracts a comment from a JSON map.
func parseComment(comment map[string]any) *Comment {
	user, userID := "", ""
	if u, ok := comment["user"].(map[string]any); ok {
		user = str(u, "login")
		if user == "" {
			user = str(u, "username")
		}
		userID = idStr(u, "id")
	}
	return &Comment{
		ID:                  int64n(comment, "id"),
		Body:                str(comment, "body"),
		User:                user,
		UserID:              userID,
		Path:                str(comment, "path"),
		Line:                int(int64n(comment, "line")),
		PullRequestReviewID: int64n(comment, "pull_request_review_id"),
	}
}
