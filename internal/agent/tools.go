package agent

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Default available build/validation tools.
var DefaultAvailableTools = []string{
	"mvn", "gradle", "go", "npm", "npx", "yarn", "pnpm",
	"python3", "pip", "pytest",
	"cargo", "rustc",
	"gcc", "g++", "make", "cmake",
	"ruby", "bundle", "rake",
	"dotnet",
	"tsc",
}

// ToolConfig holds configuration for tool execution.
type ToolConfig struct {
	AvailableTools []string
	TimeoutSeconds int
	BuildEnabled   bool
}

func DefaultToolConfig() ToolConfig {
	return ToolConfig{
		AvailableTools: DefaultAvailableTools,
		TimeoutSeconds: 300,
		BuildEnabled:   true,
	}
}

// WorkspaceResult holds the result of workspace preparation.
type WorkspaceResult struct {
	Success       bool
	WorkspacePath string
	Error         string
}

// ToolResult holds the result of a tool execution.
type ToolResult struct {
	Success  bool
	ExitCode int
	Output   string
	Error    string
}

// FormatForAI formats the tool result for sending to the AI.
func (r ToolResult) FormatForAI() string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Exit code: %d\n", r.ExitCode)
	if r.Error != "" {
		fmt.Fprintf(&sb, "Error: %s\n", r.Error)
	}
	if r.Output != "" {
		fmt.Fprintf(&sb, "Output:\n```\n%s```\n", r.Output)
	}
	return sb.String()
}

// ToolExecutor handles workspace preparation and tool execution for agent validation.
type ToolExecutor struct {
	cfg ToolConfig
}

func NewToolExecutor(cfg ToolConfig) *ToolExecutor {
	return &ToolExecutor{cfg: cfg}
}

// GetAvailableTools returns the list of tools the AI can request.
func (e *ToolExecutor) GetAvailableTools() []string {
	return e.cfg.AvailableTools
}

// PrepareWorkspace clones the repository and applies file changes.
func (e *ToolExecutor) PrepareWorkspace(ctx context.Context, owner, repo, branch string, fileChanges []FileChange, cloneURL, token string) WorkspaceResult {
	tmpDir, err := os.MkdirTemp("", "agent-validation-")
	if err != nil {
		return WorkspaceResult{Error: "Failed to create temp dir: " + err.Error()}
	}

	slog.Info("Cloning repository for validation", "dir", tmpDir, "branch", branch)

	authCloneURL := buildCloneURL(owner, repo, cloneURL, token)

	cloneCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cloneCtx, "git", "clone", "--depth", "1", "--branch", branch, authCloneURL, tmpDir)
	cmd.Dir = filepath.Dir(tmpDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		os.RemoveAll(tmpDir)
		return WorkspaceResult{Error: fmt.Sprintf("Failed to clone repository: %s — %s", err, stderr.String())}
	}

	// Apply file changes
	for _, fc := range fileChanges {
		filePath := filepath.Join(tmpDir, fc.Path)
		switch strings.ToUpper(fc.Operation) {
		case "CREATE", "UPDATE":
			if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
				slog.Warn("Failed to create directory for file", "path", fc.Path, "err", err)
				continue
			}
			if err := os.WriteFile(filePath, []byte(fc.Content), 0644); err != nil {
				slog.Warn("Failed to write file", "path", fc.Path, "err", err)
			}
		case "DELETE":
			os.Remove(filePath)
		}
	}

	return WorkspaceResult{Success: true, WorkspacePath: tmpDir}
}

// ExecuteTool runs a tool command in the workspace directory.
func (e *ToolExecutor) ExecuteTool(ctx context.Context, workspaceDir, tool string, args []string) ToolResult {
	// Validate tool is allowed
	allowed := false
	for _, t := range e.cfg.AvailableTools {
		if t == tool {
			allowed = true
			break
		}
	}
	if !allowed {
		return ToolResult{
			ExitCode: -1,
			Error:    fmt.Sprintf("Tool '%s' is not available. Available: %s", tool, strings.Join(e.cfg.AvailableTools, ", ")),
		}
	}

	slog.Info("Executing validation tool", "tool", tool, "args", args, "workspace", workspaceDir)

	timeout := time.Duration(e.cfg.TimeoutSeconds) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmdArgs := append([]string{}, args...)
	cmd := exec.CommandContext(execCtx, tool, cmdArgs...)
	cmd.Dir = workspaceDir
	cmd.Env = append(os.Environ(),
		"HOME="+workspaceDir,
		"CI=true", // Many tools behave better in CI mode
	)

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()

	if execCtx.Err() == context.DeadlineExceeded {
		return ToolResult{
			ExitCode: -1,
			Error:    fmt.Sprintf("Tool execution timed out after %d seconds", e.cfg.TimeoutSeconds),
		}
	}

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return ToolResult{
				ExitCode: -1,
				Error:    "Failed to execute tool: " + err.Error(),
			}
		}
	}

	// Truncate output if too long
	outputStr := output.String()
	if len(outputStr) > 10000 {
		outputStr = outputStr[:10000] + "\n... (output truncated)"
	}

	success := exitCode == 0
	slog.Info("Tool execution completed", "tool", tool, "success", success, "exit_code", exitCode)

	return ToolResult{
		Success:  success,
		ExitCode: exitCode,
		Output:   outputStr,
	}
}

// CleanupWorkspace removes the workspace directory.
func (e *ToolExecutor) CleanupWorkspace(workspaceDir string) {
	if workspaceDir != "" {
		os.RemoveAll(workspaceDir)
		slog.Debug("Cleaned up workspace", "dir", workspaceDir)
	}
}

// UpdateWorkspaceFiles applies new file changes to an existing workspace.
func (e *ToolExecutor) UpdateWorkspaceFiles(workspaceDir string, fileChanges []FileChange) {
	for _, fc := range fileChanges {
		filePath := filepath.Join(workspaceDir, fc.Path)
		switch strings.ToUpper(fc.Operation) {
		case "CREATE", "UPDATE":
			os.MkdirAll(filepath.Dir(filePath), 0755)
			os.WriteFile(filePath, []byte(fc.Content), 0644)
		case "DELETE":
			os.Remove(filePath)
		}
	}
}

func buildCloneURL(owner, repo, cloneBaseURL, token string) string {
	protocol := "https"
	if strings.HasPrefix(cloneBaseURL, "http://") {
		protocol = "http"
	}
	baseURL := strings.TrimPrefix(strings.TrimPrefix(cloneBaseURL, "https://"), "http://")
	baseURL = strings.TrimSuffix(baseURL, "/")

	return fmt.Sprintf("%s://oauth2:%s@%s/%s/%s.git", protocol, token, baseURL, owner, repo)
}
