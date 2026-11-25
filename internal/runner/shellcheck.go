package runner

import (
	"fmt"
	"regexp"
	"strings"
)

// alwaysAllowedCommands are shell builtins that only move around or print, so
// chains like "cd web && npm test" work with any allowlist.
var alwaysAllowedCommands = map[string]bool{
	"cd": true, "pwd": true, "echo": true, "printf": true, "true": true, "false": true, "test": true, "[": true,
}

var (
	envAssignmentRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	// redirectionRe matches a redirection word: 2>&1, >out, &>log, <in, >>x.
	redirectionRe = regexp.MustCompile(`^[0-9]*(>>?|<<?|&>>?|>&|<&)`)
	// redirectionOpRe matches a redirection operator whose target is the next word.
	redirectionOpRe = regexp.MustCompile(`^[0-9]*(>>?|<<?|&>>?|>&|<&)$`)
)

// shellCommandNames returns the command name of every simple command in a
// shell line: the line is split on ;, &&, ||, |, & and newlines outside
// quotes, and leading VAR=value assignments, "!" and grouping are skipped.
// Command substitution ($(...) or backticks) can run anything, so it is
// refused.
//
// This makes the allowlist hold for chains, not only for the first word. It
// is still not a sandbox: an allowed command can itself run others (find
// -exec, xargs, make, npm scripts).
func shellCommandNames(line string) ([]string, error) {
	var names []string
	var word strings.Builder
	inSingle, inDouble, escaped := false, false, false
	atStart := true // the next word is a command name
	wordStarted := false
	skipTarget := false // the next word is a redirection target

	endWord := func() {
		if !wordStarted {
			return
		}
		w := word.String()
		word.Reset()
		wordStarted = false
		if !atStart {
			return
		}
		if skipTarget {
			skipTarget = false
			return
		}
		switch {
		case redirectionRe.MatchString(w):
			skipTarget = redirectionOpRe.MatchString(w) // ">" alone: the target follows
			return
		case w == "!" || w == "{" || w == "(":
			return // still at the start of a command
		case envAssignmentRe.MatchString(w):
			return // VAR=value before the command
		}
		names = append(names, w)
		atStart = false
	}

	for i := 0; i < len(line); i++ {
		c := line[i]
		switch {
		case escaped:
			word.WriteByte(c)
			wordStarted = true
			escaped = false
		case c == '\\' && !inSingle:
			escaped = true
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			wordStarted = true
		case c == '"' && !inSingle:
			inDouble = !inDouble
			wordStarted = true
		case !inSingle && (c == '`' || (c == '$' && i+1 < len(line) && line[i+1] == '(')):
			return nil, fmt.Errorf("command substitution ($(...) or backticks) is not allowed; run the commands one at a time")
		case inSingle || inDouble:
			word.WriteByte(c)
		case c == '&' && ((i > 0 && (line[i-1] == '>' || line[i-1] == '<')) || (i+1 < len(line) && line[i+1] == '>')):
			// Redirections 2>&1, >&2, &>file: part of the word, not a separator.
			word.WriteByte(c)
			wordStarted = true
		case c == ';' || c == '&' || c == '|' || c == '\n' || c == '(' || c == ')':
			endWord()
			atStart = true
		case c == ' ' || c == '\t':
			endWord()
		default:
			word.WriteByte(c)
			wordStarted = true
		}
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote in command")
	}
	endWord()
	return names, nil
}

// checkShellAllowlist reports the first command in line that is neither in
// allowed nor an always-allowed builtin. An empty allowlist allows everything.
func checkShellAllowlist(line string, allowed map[string]bool) error {
	if len(allowed) == 0 {
		return nil
	}
	names, err := shellCommandNames(line)
	if err != nil {
		return err
	}
	for _, n := range names {
		if !allowed[n] && !alwaysAllowedCommands[n] {
			return fmt.Errorf("command %q not allowed", n)
		}
	}
	return nil
}
