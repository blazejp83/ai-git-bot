package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/tmseidel/ai-git-bot/internal/ai"
	"github.com/tmseidel/ai-git-bot/internal/repo"
	"github.com/tmseidel/ai-git-bot/internal/webhook"
)

var (
	jsonBlockPattern  = regexp.MustCompile(`(?s)` + "```json\\s*\\n(.*?)\\n\\s*```")
	jsonObjectPattern = regexp.MustCompile(`(?s)(\\{\\s*"summary"\\s*:.*)`)
)

// AgentConfig holds configuration for the agent service.
type AgentConfig struct {
	MaxFiles            int
	MaxTokens           int
	BranchPrefix        string
	MaxFileContentChars int
	MaxTreeFiles        int
	ValidationEnabled   bool
	MaxRetries          int
}

// FileChange represents a single file modification.
type FileChange struct {
	Path      string `json:"path"`
	Operation string `json:"operation"` // CREATE, UPDATE, DELETE
	Content   string `json:"content"`
}

// ImplementationPlan is the JSON structure the AI returns.
type ImplementationPlan struct {
	Summary      string       `json:"summary"`
	FileChanges  []FileChange `json:"fileChanges"`
	RequestFiles []string     `json:"requestFiles"`
	RunTool      *ToolRequest `json:"runTool"`
	Message      string       `json:"message"`
	Done         bool         `json:"done"`
}

func (p *ImplementationPlan) hasFileChanges() bool {
	return p != nil && len(p.FileChanges) > 0
}

func (p *ImplementationPlan) hasToolRequest() bool {
	return p != nil && p.RunTool != nil && p.RunTool.Tool != ""
}

func (p *ImplementationPlan) hasFileRequests() bool {
	return p != nil && len(p.RequestFiles) > 0
}

// ToolRequest represents a validation tool invocation.
type ToolRequest struct {
	Tool string   `json:"tool"`
	Args []string `json:"args"`
}

// Service handles issue implementation (the agent feature).
type Service struct {
	repoClient repo.Client
	aiClient   ai.Client
	sessions   *SessionService
	cfg        AgentConfig
	promptText string
}

func NewService(repoClient repo.Client, aiClient ai.Client, sessions *SessionService, cfg AgentConfig, promptText string) *Service {
	return &Service{
		repoClient: repoClient,
		aiClient:   aiClient,
		sessions:   sessions,
		cfg:        cfg,
		promptText: promptText,
	}
}

// HandleIssueAssigned is called when an issue is assigned to the bot.
func (s *Service) HandleIssueAssigned(ctx context.Context, event *webhook.Event) {
	owner := event.Repo.Owner
	repoName := event.Repo.Name
	issue := event.Issue
	if issue == nil {
		return
	}

	slog.Info("Starting implementation", "issue", issue.Number, "title", issue.Title, "repo", owner+"/"+repoName)

	// Check for existing session
	existing, _ := s.sessions.GetByIssue(owner, repoName, issue.Number)
	if existing != nil {
		slog.Info("Session already exists for issue, skipping", "issue", issue.Number)
		return
	}

	session, err := s.sessions.Create(owner, repoName, issue.Number, issue.Title)
	if err != nil {
		slog.Error("Failed to create agent session", "err", err)
		return
	}

	var branchName string

	defer func() {
		if r := recover(); r != nil {
			slog.Error("Agent panicked", "issue", issue.Number, "err", r)
			s.sessions.SetStatus(session, "FAILED")
		}
	}()

	s.repoClient.PostComment(ctx, owner, repoName, issue.Number,
		"**AI Agent**: I've been assigned to this issue. Analyzing repository structure...")

	// Get base branch
	baseBranch, _ := s.repoClient.GetDefaultBranch(ctx, owner, repoName)
	if baseBranch == "" {
		baseBranch = "main"
	}

	// Fetch repo tree
	tree, err := s.repoClient.GetRepoTree(ctx, owner, repoName, baseBranch)
	if err != nil {
		slog.Error("Failed to fetch repo tree", "err", err)
		s.failIssue(ctx, session, owner, repoName, issue.Number, branchName, "Failed to fetch repository tree: "+err.Error())
		return
	}
	treeContext := buildTreeContext(tree, s.cfg.MaxTreeFiles)

	// Step 1: Ask AI which files it needs
	slog.Info("Step 1: Asking AI which files are needed", "issue", issue.Number)
	fileReqPrompt := buildFileRequestPrompt(issue.Title, issue.Body, treeContext)
	fileReqResp, err := s.aiClient.Chat(ctx, nil, fileReqPrompt, ai.ChatOpts{
		SystemPrompt: s.promptText, MaxTokensOverride: s.cfg.MaxTokens,
	})
	if err != nil {
		s.failIssue(ctx, session, owner, repoName, issue.Number, branchName, "AI failed to respond: "+err.Error())
		return
	}

	requestedFiles := parseRequestedFiles(fileReqResp, tree)
	slog.Info("AI requested files", "count", len(requestedFiles))
	fileContext := s.fetchFiles(ctx, owner, repoName, baseBranch, requestedFiles)

	// Step 2: Generate implementation
	slog.Info("Step 2: Generating implementation", "issue", issue.Number)
	implPrompt := buildImplementationPrompt(issue.Title, issue.Body, treeContext, fileContext)

	plan := s.generateValidated(ctx, session, implPrompt, owner, repoName, issue.Number, baseBranch)

	if !plan.hasFileChanges() {
		s.sessions.SetStatus(session, "FAILED")
		s.repoClient.PostComment(ctx, owner, repoName, issue.Number,
			"**AI Agent**: I was unable to generate a valid implementation plan for this issue.")
		return
	}

	if len(plan.FileChanges) > s.cfg.MaxFiles {
		s.sessions.SetStatus(session, "FAILED")
		s.repoClient.PostComment(ctx, owner, repoName, issue.Number,
			fmt.Sprintf("**AI Agent**: The plan requires %d file changes, but the maximum is %d.", len(plan.FileChanges), s.cfg.MaxFiles))
		return
	}

	// Create branch and apply changes
	branchName = s.cfg.BranchPrefix + fmt.Sprintf("issue-%d", issue.Number)
	s.sessions.SetBranch(session, branchName)

	if err := s.repoClient.CreateBranch(ctx, owner, repoName, branchName, baseBranch); err != nil {
		s.failIssue(ctx, session, owner, repoName, issue.Number, branchName, "Failed to create branch: "+err.Error())
		return
	}

	for _, fc := range plan.FileChanges {
		commitMsg := fmt.Sprintf("agent: %s %s (issue #%d)", strings.ToLower(fc.Operation), fc.Path, issue.Number)
		if err := s.applyFileChange(ctx, owner, repoName, branchName, baseBranch, fc, commitMsg); err != nil {
			slog.Error("Failed to apply file change", "path", fc.Path, "err", err)
		}
	}

	// Create PR
	prTitle := fmt.Sprintf("AI Agent: %s (fixes #%d)", issue.Title, issue.Number)
	prBody := buildPRBody(issue.Number, plan)
	prNumber, err := s.repoClient.CreatePR(ctx, owner, repoName, prTitle, prBody, branchName, baseBranch)
	if err != nil {
		s.failIssue(ctx, session, owner, repoName, issue.Number, branchName, "Failed to create PR: "+err.Error())
		return
	}

	s.sessions.SetPR(session, prNumber)
	s.sessions.SetStatus(session, "PR_CREATED")

	// Post success comment
	var fileList strings.Builder
	for _, fc := range plan.FileChanges {
		fmt.Fprintf(&fileList, "- `%s` (%s)\n", fc.Path, fc.Operation)
	}

	s.repoClient.PostComment(ctx, owner, repoName, issue.Number,
		fmt.Sprintf("**AI Agent**: Implementation complete! I've created #%d with the following changes:\n\n**Summary**: %s\n\n**Files changed** (%d):\n%s",
			prNumber, plan.Summary, len(plan.FileChanges), fileList.String()))

	slog.Info("Successfully created PR for issue", "pr", prNumber, "issue", issue.Number)
}

// HandleIssueComment handles follow-up comments on issues with existing agent sessions.
func (s *Service) HandleIssueComment(ctx context.Context, event *webhook.Event) {
	owner := event.Repo.Owner
	repoName := event.Repo.Name
	issue := event.Issue
	comment := event.Comment
	if issue == nil || comment == nil {
		return
	}

	session, _ := s.sessions.GetByIssue(owner, repoName, issue.Number)
	if session == nil {
		return
	}

	slog.Info("Handling agent follow-up comment", "issue", issue.Number, "comment", comment.ID)

	s.repoClient.AddReaction(ctx, owner, repoName, comment.ID, "eyes")

	history := s.sessions.ToAiMessages(session)
	response, err := s.aiClient.Chat(ctx, history, comment.Body, ai.ChatOpts{
		SystemPrompt: s.promptText, MaxTokensOverride: s.cfg.MaxTokens,
	})
	if err != nil {
		slog.Error("AI chat failed for follow-up", "err", err)
		return
	}

	s.sessions.AddMessage(session, "user", comment.Body)
	s.sessions.AddMessage(session, "assistant", response)

	plan := parseAIResponse(response)
	if plan.hasFileChanges() && session.BranchName != "" {
		baseBranch, _ := s.repoClient.GetDefaultBranch(ctx, owner, repoName)
		for _, fc := range plan.FileChanges {
			commitMsg := fmt.Sprintf("agent: %s %s (issue #%d)", strings.ToLower(fc.Operation), fc.Path, issue.Number)
			s.applyFileChange(ctx, owner, repoName, session.BranchName, baseBranch, fc, commitMsg)
		}
		s.repoClient.PostComment(ctx, owner, repoName, issue.Number,
			fmt.Sprintf("**AI Agent**: Updated %d file(s) based on your feedback.", len(plan.FileChanges)))
	} else if response != "" {
		s.repoClient.PostComment(ctx, owner, repoName, issue.Number,
			"**AI Agent**: "+response)
	}
}

func (s *Service) generateValidated(ctx context.Context, session *AgentSessionRow, userMsg, owner, repoName string, issueNumber int64, baseBranch string) *ImplementationPlan {
	maxRetries := s.cfg.MaxRetries
	if maxRetries == 0 {
		maxRetries = 1
	}

	s.sessions.AddMessage(session, "user", userMsg)

	var history []ai.Message
	currentMsg := userMsg
	var lastValid *ImplementationPlan
	fileReqRounds := 0

	for attempt := 1; attempt <= maxRetries; attempt++ {
		slog.Info("Generating implementation", "issue", issueNumber, "attempt", attempt)

		resp, err := s.aiClient.Chat(ctx, history, currentMsg, ai.ChatOpts{
			SystemPrompt: s.promptText, MaxTokensOverride: s.cfg.MaxTokens,
		})
		if err != nil {
			slog.Error("AI generation failed", "attempt", attempt, "err", err)
			break
		}

		s.sessions.AddMessage(session, "assistant", resp)

		plan := parseAIResponse(resp)
		if plan == nil {
			break
		}

		// Handle file requests
		if plan.hasFileRequests() && !plan.hasFileChanges() && fileReqRounds < 3 {
			fileReqRounds++
			tree, _ := s.repoClient.GetRepoTree(ctx, owner, repoName, baseBranch)
			fileCtx := s.fetchFiles(ctx, owner, repoName, baseBranch, filterValidPaths(plan.RequestFiles, tree))
			filesMsg := "Here are the requested files:\n" + fileCtx + "\n\nNow implement the issue."

			history = append(history, ai.Message{Role: "user", Content: currentMsg})
			history = append(history, ai.Message{Role: "assistant", Content: resp})
			currentMsg = filesMsg
			s.sessions.AddMessage(session, "user", filesMsg)
			attempt--
			continue
		}

		if plan.hasFileChanges() {
			lastValid = plan
		}

		if !plan.hasFileChanges() {
			break
		}

		return plan
	}

	if lastValid != nil {
		return lastValid
	}
	return &ImplementationPlan{}
}

func (s *Service) applyFileChange(ctx context.Context, owner, repoName, branch, baseBranch string, fc FileChange, commitMsg string) error {
	switch strings.ToUpper(fc.Operation) {
	case "CREATE":
		return s.repoClient.CreateOrUpdateFile(ctx, owner, repoName, fc.Path, fc.Content, commitMsg, branch, "")
	case "UPDATE":
		// Get existing content and apply diff
		existing, _ := s.repoClient.GetFileContent(ctx, owner, repoName, fc.Path, branch)
		sha, _ := s.repoClient.GetFileSHA(ctx, owner, repoName, fc.Path, branch)

		content := fc.Content
		if existing != "" && strings.Contains(fc.Content, "<<<<<<< SEARCH") {
			applied, err := ApplyDiff(existing, fc.Content)
			if err != nil {
				slog.Warn("Diff apply failed, using content as-is", "path", fc.Path, "err", err)
			} else {
				content = applied
			}
		}
		return s.repoClient.CreateOrUpdateFile(ctx, owner, repoName, fc.Path, content, commitMsg, branch, sha)
	case "DELETE":
		sha, _ := s.repoClient.GetFileSHA(ctx, owner, repoName, fc.Path, branch)
		return s.repoClient.DeleteFile(ctx, owner, repoName, fc.Path, commitMsg, branch, sha)
	default:
		return fmt.Errorf("unknown operation: %s", fc.Operation)
	}
}

func (s *Service) fetchFiles(ctx context.Context, owner, repoName, ref string, paths []string) string {
	var sb strings.Builder
	for _, path := range paths {
		content, err := s.repoClient.GetFileContent(ctx, owner, repoName, path, ref)
		if err != nil {
			continue
		}
		if len(content) > s.cfg.MaxFileContentChars {
			content = content[:s.cfg.MaxFileContentChars] + "\n... (truncated)"
		}
		fmt.Fprintf(&sb, "\n### `%s`\n```\n%s\n```\n", path, content)
	}
	return sb.String()
}

func (s *Service) failIssue(ctx context.Context, session *AgentSessionRow, owner, repoName string, issueNumber int64, branchName, errMsg string) {
	s.sessions.SetStatus(session, "FAILED")
	if branchName != "" {
		s.repoClient.DeleteBranch(ctx, owner, repoName, branchName)
	}
	s.repoClient.PostComment(ctx, owner, repoName, issueNumber,
		fmt.Sprintf("**AI Agent**: Implementation failed: `%s`\n\nYou can mention me in a comment to try again.", errMsg))
}

// --- helpers ---

func parseAIResponse(response string) *ImplementationPlan {
	// Try ```json block
	matches := jsonBlockPattern.FindStringSubmatch(response)
	var jsonStr string
	if len(matches) > 1 {
		jsonStr = matches[1]
	} else {
		// Try raw JSON object
		idx := strings.Index(response, `{"summary"`)
		if idx >= 0 {
			jsonStr = response[idx:]
		}
	}

	if jsonStr == "" {
		return nil
	}

	var plan ImplementationPlan
	if err := json.Unmarshal([]byte(jsonStr), &plan); err != nil {
		slog.Warn("Failed to parse implementation plan JSON", "err", err)
		return nil
	}
	return &plan
}

func buildTreeContext(tree []repo.TreeEntry, maxFiles int) string {
	var sb strings.Builder
	sb.WriteString("Repository file tree:\n```\n")
	count := 0
	for _, e := range tree {
		if count >= maxFiles {
			sb.WriteString("... (truncated)\n")
			break
		}
		sb.WriteString(e.Path)
		sb.WriteByte('\n')
		count++
	}
	sb.WriteString("```\n")
	return sb.String()
}

func buildFileRequestPrompt(title, body, treeContext string) string {
	return fmt.Sprintf(`I need to implement the following issue:

**Title**: %s
**Description**: %s

%s

Which files do I need to read to implement this? List the file paths, one per line.`, title, body, treeContext)
}

func buildImplementationPrompt(title, body, treeContext, fileContext string) string {
	return fmt.Sprintf(`Implement the following issue:

**Title**: %s
**Description**: %s

%s

**Relevant file contents**:
%s

Respond with a JSON object containing:
- "summary": brief description of changes
- "fileChanges": array of {"path", "operation" (CREATE/UPDATE/DELETE), "content"}
- For UPDATE operations, use SEARCH/REPLACE blocks in content`, title, body, treeContext, fileContext)
}

func buildPRBody(issueNumber int64, plan *ImplementationPlan) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Fixes #%d\n\n", issueNumber)
	sb.WriteString("## Summary\n\n")
	sb.WriteString(plan.Summary)
	sb.WriteString("\n\n## Changes\n\n")
	for _, fc := range plan.FileChanges {
		fmt.Fprintf(&sb, "- `%s` (%s)\n", fc.Path, fc.Operation)
	}
	sb.WriteString("\n---\n*Generated by AI Agent*\n")
	return sb.String()
}

func parseRequestedFiles(response string, tree []repo.TreeEntry) []string {
	validPaths := make(map[string]bool)
	for _, e := range tree {
		validPaths[e.Path] = true
	}

	var files []string
	for _, line := range strings.Split(response, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "- ")
		line = strings.TrimPrefix(line, "* ")
		line = strings.Trim(line, "`")
		if validPaths[line] {
			files = append(files, line)
		}
	}
	return files
}

func filterValidPaths(paths []string, tree []repo.TreeEntry) []string {
	valid := make(map[string]bool)
	for _, e := range tree {
		valid[e.Path] = true
	}
	var result []string
	for _, p := range paths {
		if valid[p] {
			result = append(result, p)
		}
	}
	return result
}
