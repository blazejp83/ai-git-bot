package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

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
	repoClient   repo.Client
	aiClient     ai.Client
	sessions     *SessionService
	toolExecutor *ToolExecutor
	cfg          AgentConfig
	promptText   string
}

func NewService(repoClient repo.Client, aiClient ai.Client, sessions *SessionService, cfg AgentConfig, promptText string) *Service {
	toolCfg := DefaultToolConfig()
	if cfg.ValidationEnabled {
		toolCfg.BuildEnabled = true
	}
	if cfg.MaxRetries > 0 {
		toolCfg.TimeoutSeconds = 300
	}
	return &Service{
		repoClient:   repoClient,
		aiClient:     aiClient,
		sessions:     sessions,
		toolExecutor: NewToolExecutor(toolCfg),
		cfg:          cfg,
		promptText:   promptText,
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
	maxToolExecs := 3

	// Add available tools info
	if s.cfg.ValidationEnabled {
		tools := s.toolExecutor.GetAvailableTools()
		userMsg += "\n\n**Available validation tools**: " + strings.Join(tools, ", ")
	}

	s.sessions.AddMessage(session, "user", userMsg)

	var history []ai.Message
	currentMsg := userMsg
	var lastValid *ImplementationPlan
	fileReqRounds := 0
	toolExecs := 0
	var workspaceDir string

	defer func() {
		s.toolExecutor.CleanupWorkspace(workspaceDir)
	}()

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
		s.postThinkingComment(ctx, owner, repoName, issueNumber, resp)

		plan := parseAIResponse(resp)
		if plan == nil {
			return lastValid
		}

		// Handle file requests
		if plan.hasFileRequests() && !plan.hasFileChanges() && fileReqRounds < 3 {
			fileReqRounds++
			tree, _ := s.repoClient.GetRepoTree(ctx, owner, repoName, baseBranch)
			fileCtx := s.fetchFiles(ctx, owner, repoName, baseBranch, filterValidPaths(plan.RequestFiles, tree))
			filesMsg := "Here are the requested files:\n" + fileCtx +
				"\n\nNow implement the issue. Output JSON with fileChanges and runTool for validation."

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

		if !plan.hasFileChanges() && !plan.hasToolRequest() {
			return lastValid
		}

		// Skip validation if disabled
		if !s.cfg.ValidationEnabled {
			return plan
		}

		// Tool execution loop
		if plan.hasToolRequest() && plan.hasFileChanges() && toolExecs < maxToolExecs {
			toolExecs++

			// Prepare or update workspace
			if workspaceDir == "" {
				workspaceDir = s.prepareLocalWorkspace(ctx, owner, repoName, baseBranch, plan.FileChanges)
				if workspaceDir == "" {
					slog.Error("Failed to prepare workspace")
					return plan
				}
			} else {
				s.toolExecutor.UpdateWorkspaceFiles(workspaceDir, plan.FileChanges)
			}

			toolReq := plan.RunTool
			slog.Info("AI requested validation tool", "tool", toolReq.Tool, "args", toolReq.Args)

			result := s.toolExecutor.ExecuteTool(ctx, workspaceDir, toolReq.Tool, toolReq.Args)

			// Post tool result
			s.postToolComment(ctx, owner, repoName, issueNumber, toolReq, result)

			if result.Success {
				slog.Info("Validation succeeded", "attempt", attempt)
				return plan
			}

			// Build feedback for AI
			feedback := buildToolFeedback(toolReq, result)
			if lastValid != nil {
				feedback += buildPreviousChangesInfo(lastValid)
			}

			history = append(history, ai.Message{Role: "user", Content: currentMsg})
			history = append(history, ai.Message{Role: "assistant", Content: resp})
			currentMsg = feedback
			s.sessions.AddMessage(session, "user", feedback)
			continue
		}

		// Has file changes but no tool request — ask AI to add one
		if plan.hasFileChanges() && !plan.hasToolRequest() {
			feedback := buildMissingToolFeedback()
			history = append(history, ai.Message{Role: "user", Content: currentMsg})
			history = append(history, ai.Message{Role: "assistant", Content: resp})
			currentMsg = feedback
			s.sessions.AddMessage(session, "user", feedback)
			continue
		}

		return plan
	}

	if lastValid != nil {
		return lastValid
	}
	return &ImplementationPlan{}
}

func (s *Service) prepareLocalWorkspace(ctx context.Context, owner, repoName, branch string, fileChanges []FileChange) string {
	tmpDir, err := os.MkdirTemp("", "agent-validation-")
	if err != nil {
		return ""
	}

	// Try to clone the repo
	cloneCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	// Get credentials from the repo client for cloning
	// We use a generic approach: just create the workspace with files
	cmd := exec.CommandContext(cloneCtx, "git", "init", tmpDir)
	if err := cmd.Run(); err != nil {
		// Fallback: just create directory structure
		slog.Debug("Git init failed, using plain directory", "err", err)
	}

	// Apply file changes
	for _, fc := range fileChanges {
		filePath := filepath.Join(tmpDir, fc.Path)
		switch strings.ToUpper(fc.Operation) {
		case "CREATE", "UPDATE":
			os.MkdirAll(filepath.Dir(filePath), 0755)
			os.WriteFile(filePath, []byte(fc.Content), 0644)
		case "DELETE":
			os.Remove(filePath)
		}
	}

	return tmpDir
}

func (s *Service) postThinkingComment(ctx context.Context, owner, repoName string, issueNumber int64, response string) {
	// Extract non-JSON reasoning text
	thinking := response
	if idx := strings.Index(thinking, "```json"); idx >= 0 {
		thinking = strings.TrimSpace(thinking[:idx])
	}
	if thinking != "" && len(thinking) > 20 {
		// Truncate long thinking
		if len(thinking) > 2000 {
			thinking = thinking[:2000] + "\n... (truncated)"
		}
		s.repoClient.PostComment(ctx, owner, repoName, issueNumber,
			"**AI Agent** (thinking):\n\n"+thinking)
	}
}

func (s *Service) postToolComment(ctx context.Context, owner, repoName string, issueNumber int64, toolReq *ToolRequest, result ToolResult) {
	var comment strings.Builder
	fmt.Fprintf(&comment, "**Tool Execution**: `%s", toolReq.Tool)
	if len(toolReq.Args) > 0 {
		comment.WriteString(" " + strings.Join(toolReq.Args, " "))
	}
	comment.WriteString("`\n\n")

	if result.Success {
		comment.WriteString("**Success**\n")
	} else {
		fmt.Fprintf(&comment, "**Failed** (exit code %d)\n", result.ExitCode)
		if result.Output != "" {
			output := result.Output
			if len(output) > 2000 {
				output = output[:2000] + "\n... (truncated)"
			}
			fmt.Fprintf(&comment, "\n```\n%s```\n", output)
		}
	}

	s.repoClient.PostComment(ctx, owner, repoName, issueNumber, comment.String())
}

func buildToolFeedback(toolReq *ToolRequest, result ToolResult) string {
	var sb strings.Builder
	sb.WriteString("## Tool Execution Result\n\n")
	fmt.Fprintf(&sb, "**Command**: `%s", toolReq.Tool)
	if len(toolReq.Args) > 0 {
		sb.WriteString(" " + strings.Join(toolReq.Args, " "))
	}
	sb.WriteString("`\n\n")

	if result.Success {
		sb.WriteString("**Success** (exit code 0)\n")
	} else {
		fmt.Fprintf(&sb, "**Failed** (exit code %d)\n", result.ExitCode)
	}

	sb.WriteString("\n" + result.FormatForAI())

	if !result.Success {
		sb.WriteString("\nFix the errors and provide updated `fileChanges`. Include `runTool` to validate again.")
	}

	return sb.String()
}

func buildPreviousChangesInfo(plan *ImplementationPlan) string {
	if !plan.hasFileChanges() {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n\n## IMPORTANT: Preserve Previous Changes\n\n")
	sb.WriteString("Your previous response included the following file changes that need to be preserved.\n")
	sb.WriteString("When fixing errors, you MUST include ALL these changes, not just the fix.\n\n")
	fmt.Fprintf(&sb, "**Files from your previous response** (%d):\n", len(plan.FileChanges))

	for _, fc := range plan.FileChanges {
		fmt.Fprintf(&sb, "- `%s` (%s)\n", fc.Path, fc.Operation)
	}

	sb.WriteString("\nInclude all these files in your `fileChanges` array, updating any that need fixes.\n")
	return sb.String()
}

func buildMissingToolFeedback() string {
	return `## Missing Validation Tool

Your response included fileChanges but no runTool for validation.

**Validation is mandatory.** Please provide the same file changes again,
but this time include a runTool to validate the code.

Detect the build system from the file tree and request the appropriate tool:
- Maven: {"tool": "mvn", "args": ["compile", "-q", "-B"]}
- Gradle: {"tool": "gradle", "args": ["compileJava", "-q"]}
- npm: {"tool": "npm", "args": ["run", "build"]}
- Go: {"tool": "go", "args": ["build", "./..."]}
- Cargo: {"tool": "cargo", "args": ["build"]}
- Make: {"tool": "make", "args": []}

Output JSON with both fileChanges and runTool.`
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
