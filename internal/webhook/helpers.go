package webhook

// str extracts a string from a JSON map.
func str(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
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
	return User{Login: login}
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

	if head, ok := pr["head"].(map[string]any); ok {
		result.Head = Ref{RefName: str(head, "ref"), SHA: str(head, "sha")}
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

	if assignee, ok := issue["assignee"].(map[string]any); ok {
		result.Assignee = str(assignee, "login")
		if result.Assignee == "" {
			result.Assignee = str(assignee, "username")
		}
	}
	if assignees, ok := issue["assignees"].([]any); ok {
		for _, a := range assignees {
			if am, ok := a.(map[string]any); ok {
				login := str(am, "login")
				if login == "" {
					login = str(am, "username")
				}
				result.Assignees = append(result.Assignees, login)
			}
		}
	}
	_, result.IsPR = issue["pull_request"]

	return result
}

// parseComment extracts a comment from a JSON map.
func parseComment(comment map[string]any) *Comment {
	user := ""
	if u, ok := comment["user"].(map[string]any); ok {
		user = str(u, "login")
		if user == "" {
			user = str(u, "username")
		}
	}
	return &Comment{
		ID:                  int64n(comment, "id"),
		Body:                str(comment, "body"),
		User:                user,
		Path:                str(comment, "path"),
		Line:                int(int64n(comment, "line")),
		PullRequestReviewID: int64n(comment, "pull_request_review_id"),
	}
}
