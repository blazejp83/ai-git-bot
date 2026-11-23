package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

// ToolRegistry holds all available tools and executes them.
type ToolRegistry struct {
	tools          map[string]RegisteredTool
	aliases        map[string]string
	shellAllowlist map[string]bool
	shellTimeout   time.Duration
}

// RegisteredTool pairs a definition with an executor.
type RegisteredTool struct {
	Def     ai.ToolDef
	Execute func(ctx context.Context, ws *Workspace, input map[string]any) (string, error)
}

func NewToolRegistry(mode Mode, shellAllowlist []string, shellTimeout time.Duration, prDiff string) *ToolRegistry {
	r := &ToolRegistry{
		tools:          make(map[string]RegisteredTool),
		aliases:        map[string]string{"search": "rg", "grep": "rg", "ripgrep": "rg", "pr-diff": "pr_diff"},
		shellAllowlist: make(map[string]bool),
		shellTimeout:   shellTimeout,
	}
	for _, cmd := range shellAllowlist {
		r.shellAllowlist[cmd] = true
	}

	// Read-only tools (available in all modes)
	r.register(RegisteredTool{
		Def: ai.ToolDef{
			Name:        "read_file",
			Description: "Read the contents of a file in the repository.",
			Parameters: jsonSchema(map[string]any{
				"path": map[string]any{"type": "string", "description": "Relative file path"},
			}, []string{"path"}),
		},
		Execute: func(ctx context.Context, ws *Workspace, input map[string]any) (string, error) {
			path, _ := input["path"].(string)
			content, err := ws.ReadFile(path)
			if err != nil {
				return "", err
			}
			if len(content) > 100000 {
				content = content[:100000] + "\n... (truncated, file too large)"
			}
			return content, nil
		},
	})

	r.register(RegisteredTool{
		Def: ai.ToolDef{
			Name:        "list_files",
			Description: "List files in the repository. Optionally filter by directory and glob pattern.",
			Parameters: jsonSchema(map[string]any{
				"path":    map[string]any{"type": "string", "description": "Directory to list (default: root)"},
				"pattern": map[string]any{"type": "string", "description": "Glob pattern to filter (e.g. *.go, *.ts)"},
			}, nil),
		},
		Execute: func(ctx context.Context, ws *Workspace, input map[string]any) (string, error) {
			dir, _ := input["path"].(string)
			pattern, _ := input["pattern"].(string)
			files, err := ws.ListFiles(dir, pattern)
			if err != nil {
				return "", err
			}
			if len(files) > 500 {
				result := strings.Join(files[:500], "\n")
				return result + fmt.Sprintf("\n... (%d more files)", len(files)-500), nil
			}
			return strings.Join(files, "\n"), nil
		},
	})

	r.register(RegisteredTool{
		Def: ai.ToolDef{
			Name:        "rg",
			Description: rgDescription,
			Parameters: jsonSchema(map[string]any{
				"args": map[string]any{
					"type":        "array",
					"items":       map[string]any{"type": "string"},
					"description": "Positional CLI arguments, one element per token. Pass an empty array for no args.",
				},
			}, []string{"args"}),
		},
		Execute: func(ctx context.Context, ws *Workspace, input map[string]any) (string, error) {
			return ws.runRg(rgArgsFromInput(input))
		},
	})

	if prDiff != "" {
		r.register(RegisteredTool{
			Def: ai.ToolDef{
				Name: "pr_diff",
				Description: `Return the diff hunks of changed files in the current pull request. Use this to see what exactly was added, removed, or modified in a file. ` +
					`Pass up to 8 paths in one call to read related files together; with more than one path, each gets a "### path" heading. ` +
					`Added and context lines carry their line number in the new file ("37 | +code"); removed lines have none.`,
				Parameters: jsonSchema(map[string]any{
					"paths": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Repository-relative paths of changed files, up to 8.",
					},
				}, []string{"paths"}),
			},
			Execute: func(ctx context.Context, ws *Workspace, input map[string]any) (string, error) {
				return renderPRDiff(prDiff, stringList(input, "paths", "path"))
			},
		})
	}

	r.register(RegisteredTool{
		Def: ai.ToolDef{
			Name:        "shell",
			Description: "Run a shell command in the repository workspace. Use for building, testing, or any command-line operation.",
			Parameters: jsonSchema(map[string]any{
				"command": map[string]any{"type": "string", "description": "Shell command to execute"},
			}, []string{"command"}),
		},
		Execute: func(ctx context.Context, ws *Workspace, input map[string]any) (string, error) {
			command, _ := input["command"].(string)
			if command == "" {
				return "", fmt.Errorf("empty command")
			}

			// Extract the base command to check allowlist
			parts := strings.Fields(command)
			if len(parts) > 0 && len(r.shellAllowlist) > 0 {
				if !r.shellAllowlist[parts[0]] {
					slog.Warn("BLOCKED shell command — not in allowlist",
						"command", parts[0],
						"full_command", command,
					)
					return "", fmt.Errorf("command %q not allowed. Allowed: %s",
						parts[0], strings.Join(shellAllowlistKeys(r.shellAllowlist), ", "))
				}
			}

			output, exitCode, err := ws.Shell(ctx, command, r.shellTimeout)
			if err != nil {
				return "", err
			}
			if exitCode != 0 {
				return fmt.Sprintf("Exit code: %d\n%s", exitCode, output), nil
			}
			return output, nil
		},
	})

	r.register(RegisteredTool{
		Def: ai.ToolDef{
			Name:        "done",
			Description: "Signal that you have completed the task. Provide your final result.",
			Parameters: jsonSchema(map[string]any{
				"result": map[string]any{"type": "string", "description": "Your final output (review text, summary, etc.)"},
			}, []string{"result"}),
		},
		Execute: func(ctx context.Context, ws *Workspace, input map[string]any) (string, error) {
			// Never actually executed — intercepted by the runner loop
			result, _ := input["result"].(string)
			return result, nil
		},
	})

	// Write tools (only in implementation mode)
	if mode == ModeImplementation {
		r.register(RegisteredTool{
			Def: ai.ToolDef{
				Name:        "write_file",
				Description: "Create or overwrite a file in the repository.",
				Parameters: jsonSchema(map[string]any{
					"path":    map[string]any{"type": "string", "description": "Relative file path"},
					"content": map[string]any{"type": "string", "description": "Complete file content"},
				}, []string{"path", "content"}),
			},
			Execute: func(ctx context.Context, ws *Workspace, input map[string]any) (string, error) {
				path, _ := input["path"].(string)
				content, _ := input["content"].(string)
				if err := ws.WriteFile(path, content); err != nil {
					return "", err
				}
				return fmt.Sprintf("File written: %s (%d bytes)", path, len(content)), nil
			},
		})
	}

	return r
}

func (r *ToolRegistry) register(tool RegisteredTool) {
	r.tools[tool.Def.Name] = tool
}

// GetToolDefs returns all tool definitions for passing to the AI.
func (r *ToolRegistry) GetToolDefs() []ai.ToolDef {
	// Sorted, so the tools array is byte-identical across requests and runs:
	// it is the start of the cached prompt prefix.
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	defs := make([]ai.ToolDef, 0, len(names))
	for _, name := range names {
		defs = append(defs, r.tools[name].Def)
	}
	return defs
}

// Execute runs a tool by name with the given input.
func (r *ToolRegistry) Execute(ctx context.Context, ws *Workspace, name string, input map[string]any) (string, error) {
	if alias, ok := r.aliases[name]; ok {
		if _, registered := r.tools[name]; !registered {
			name = alias
		}
	}
	tool, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("unknown tool: %s", name)
	}
	return tool.Execute(ctx, ws, input)
}

// HasTool returns true if the named tool is registered.
func (r *ToolRegistry) HasTool(name string) bool {
	_, ok := r.tools[name]
	return ok
}

func jsonSchema(properties map[string]any, required []string) map[string]any {
	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func shellAllowlistKeys(m map[string]bool) []string {
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

// marshalInput serializes tool input for logging/persistence.
func marshalInput(input map[string]any) string {
	b, _ := json.Marshal(input)
	return string(b)
}

// stringList reads a list of strings from the first present key; a single
// string is accepted as a one-element list.
func stringList(input map[string]any, keys ...string) []string {
	for _, k := range keys {
		switch v := input[k].(type) {
		case []any:
			var out []string
			for _, e := range v {
				if str, ok := e.(string); ok {
					out = append(out, str)
				}
			}
			return out
		case []string:
			return v
		case string:
			return []string{v}
		}
	}
	return nil
}

// rgArgsFromInput accepts the rg schema ({args: [...]}) and the shape of the
// old search tool ({pattern, path}), so replayed sessions keep working.
func rgArgsFromInput(input map[string]any) []string {
	if args := stringList(input, "args"); args != nil {
		return args
	}
	pattern, _ := input["pattern"].(string)
	if pattern == "" {
		return nil
	}
	args := []string{"-e", pattern}
	if path, _ := input["path"].(string); path != "" {
		args = append(args, path)
	}
	return args
}
