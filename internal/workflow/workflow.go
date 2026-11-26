// Package workflow describes the per-pull-request workflows a bot can run,
// their settings, their slash commands, and the orchestration that cancels a
// run when a newer one of the same workflow starts. The workflows themselves
// run in package bot.
package workflow

import (
	"context"
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// ParamField is one setting of a workflow; the bot form is generated from it.
type ParamField struct {
	Key     string
	Label   string
	Type    string // "text", "select" or "bool"
	Options []Option
	Default string
	Help    string
}

// Option is one choice of a select field.
type Option struct{ Value, Label string }

// Definition describes a workflow.
type Definition struct {
	Key            string
	Name           string
	Description    string
	Params         []ParamField
	Commands       []string // slash commands, e.g. "regenerate-readme"
	DefaultEnabled bool
}

// Keys of the built-in workflows.
const (
	Review     = "review"
	ReadmeSync = "readme-sync"
)

// Builtin lists the workflows in the order they run.
var Builtin = []Definition{
	{
		Key:  Review,
		Name: "Code review",
		Description: "Reviews each pull request when it is opened or updated (see Structured review above for the format). " +
			"Turned off, it still runs when asked: \"@bot #review\" or requesting the bot as reviewer.",
		Commands:       []string{"review", "fullreview"},
		DefaultEnabled: true,
	},
	{
		Key:  ReadmeSync,
		Name: "README sync",
		Description: "Updates Markdown documentation the pull request makes outdated. The agent may only edit Markdown files " +
			"matching the patterns. Comment \"@bot #regenerate-readme [what to change]\" on a pull request to run it, also when turned off.",
		Commands: []string{"regenerate-readme"},
		Params: []ParamField{
			{Key: "patterns", Label: "Files it may edit", Type: "text", Default: "README.md, docs/**/*.md",
				Help: "Comma-separated globs; only .md files matching them are edited."},
			{Key: "mode", Label: "How to deliver", Type: "select", Default: "comment", Options: []Option{
				{"comment", "Post the proposed change as a diff comment"},
				{"commit", "Commit to the pull request branch"},
				{"pr", "Open a separate pull request into the branch"},
			}},
			{Key: "trigger", Label: "When to run", Type: "select", Default: "command", Options: []Option{
				{"command", "Only when asked (#regenerate-readme)"},
				{"auto", "On every pull request update"},
			}},
		},
	},
}

// Lookup returns a workflow's definition.
func Lookup(key string) (Definition, bool) {
	for _, d := range Builtin {
		if d.Key == key {
			return d, true
		}
	}
	return Definition{}, false
}

// Settings are a bot's workflow settings: workflow key → param → value, plus
// "enabled". Missing values fall back to the definitions' defaults.
type Settings map[string]map[string]string

// Parse reads stored settings; empty or invalid JSON means all defaults.
func Parse(raw string) Settings {
	s := Settings{}
	if strings.TrimSpace(raw) != "" {
		json.Unmarshal([]byte(raw), &s)
	}
	return s
}

// JSON serialises the settings for storage.
func (s Settings) JSON() string {
	b, _ := json.Marshal(s)
	return string(b)
}

// Enabled reports whether a workflow is on.
func (s Settings) Enabled(key string) bool {
	if v, ok := s[key]["enabled"]; ok {
		return v == "true"
	}
	d, _ := Lookup(key)
	return d.DefaultEnabled
}

// Param returns a workflow setting, or its default.
func (s Settings) Param(key, name string) string {
	if v, ok := s[key][name]; ok && strings.TrimSpace(v) != "" {
		return v
	}
	d, _ := Lookup(key)
	for _, p := range d.Params {
		if p.Key == name {
			return p.Default
		}
	}
	return ""
}

// FromForm reads settings from bot form fields named "wf.<key>.enabled" and
// "wf.<key>.<param>". Select values outside their options are dropped.
func FromForm(form url.Values) Settings {
	s := Settings{}
	for _, d := range Builtin {
		m := map[string]string{"enabled": boolString(form.Get("wf."+d.Key+".enabled") == "true")}
		for _, p := range d.Params {
			v := strings.TrimSpace(form.Get("wf." + d.Key + "." + p.Key))
			if p.Type == "select" && !hasOption(p, v) {
				continue
			}
			if p.Type == "bool" {
				v = boolString(v == "true")
			}
			if v != "" {
				m[p.Key] = v
			}
		}
		s[d.Key] = m
	}
	return s
}

func hasOption(p ParamField, v string) bool {
	for _, o := range p.Options {
		if o.Value == v {
			return true
		}
	}
	return false
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// FormField is a param prepared for the bot form.
type FormField struct {
	ParamField
	Name  string // form field name
	Value string
}

// FormWorkflow is a workflow prepared for the bot form.
type FormWorkflow struct {
	Definition
	Enabled bool
	Fields  []FormField
}

// ForForm lists the workflows with their current values for the bot form.
func (s Settings) ForForm() []FormWorkflow {
	var out []FormWorkflow
	for _, d := range Builtin {
		fw := FormWorkflow{Definition: d, Enabled: s.Enabled(d.Key)}
		for _, p := range d.Params {
			fw.Fields = append(fw.Fields, FormField{ParamField: p, Name: "wf." + d.Key + "." + p.Key, Value: s.Param(d.Key, p.Key)})
		}
		out = append(out, fw)
	}
	return out
}

var commandRe = regexp.MustCompile(`(?:^|\s)[#/]([a-z][a-z-]*)\b`)

// ParseCommand finds a workflow slash command (#name or /name) in a comment.
// It returns the workflow key and the text after the command as instruction.
func ParseCommand(body string) (key, instruction string, ok bool) {
	lower := strings.ToLower(body)
	for _, m := range commandRe.FindAllStringSubmatchIndex(lower, -1) {
		name := lower[m[2]:m[3]]
		for _, d := range Builtin {
			for _, c := range d.Commands {
				if c == name {
					return d.Key, strings.TrimSpace(body[m[1]:]), true
				}
			}
		}
	}
	return "", "", false
}

// Orchestrator runs workflows so that a newer run of the same workflow on the
// same pull request cancels the older one.
type Orchestrator struct {
	mu      sync.Mutex
	running map[string]*runHandle
}

type runHandle struct{ cancel context.CancelFunc }

func NewOrchestrator() *Orchestrator { return &Orchestrator{running: map[string]*runHandle{}} }

// Start registers a run under key (bot, repository, pull request, workflow),
// cancelling a run already registered there. Call done when the run ends.
func (o *Orchestrator) Start(parent context.Context, key string) (ctx context.Context, done func()) {
	ctx, cancel := context.WithCancel(parent)
	h := &runHandle{cancel: cancel}
	o.mu.Lock()
	if prev := o.running[key]; prev != nil {
		prev.cancel()
	}
	o.running[key] = h
	o.mu.Unlock()
	return ctx, func() {
		o.mu.Lock()
		if o.running[key] == h {
			delete(o.running, key)
		}
		o.mu.Unlock()
		cancel()
	}
}

// Running lists the keys with a run in progress (for tests and diagnostics).
func (o *Orchestrator) Running() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	var keys []string
	for k := range o.running {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
