package runner

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	rgMaxMatches        = 200
	rgMaxMatchesPerFile = 5
	rgMaxDepth          = 12
	rgMaxContextLines   = 10
	rgMaxFileSize       = 1_000_000
	rgBinarySniffBytes  = 8000
	rgMaxOutputChars    = 10000
)

const rgDescription = `Search file contents across the workspace, like ripgrep. Args: ["pattern"] or ["pattern", "path", ...] (every further element is a path); the pattern is a Go (RE2) regex. Flags -i, -F, -l, -o (only the matching part, one line per match), -g <glob>, -A/-B/-C <lines> (context, max 10) and -d/--max-depth <n> (1 = directly below the path) may go anywhere, each flag and each value its own element (["-C", "3", "pattern"]); -n, -H, -r, -R, -E and their long forms (--line-number, --no-heading, ...) change nothing here and are accepted, any other flag is rejected. Use -e <pattern> or -- for a pattern starting with -; ["--files", "path"] lists files. -g takes "!glob" to exclude and {a,b} for alternatives (["-g", "!src/test/**"]).`

const rgSupported = "Supported flags: -i, -F, -l, -o, -n, -g <glob>, -e <pattern>, -A/-B/-C <lines> (max 10), -d/--max-depth <n>, --files; use -e <pattern> or -- for a pattern that starts with -."

const rgCappedNote = "Search stopped after 200 results; later lines and files were not searched and may match too. Narrow it with a path, -g <glob> or a more specific pattern."

type rgOptions struct {
	ignoreCase, fixed, filesWithMatches, onlyMatching, listFiles bool
	globs, patterns, positionals                                 []string
	before, after, contextLines                                  int
	beforeSet, afterSet                                          bool
	maxDepth                                                     int
}

var (
	rgFlagWithValueRe = regexp.MustCompile(`^-{1,2}[A-Za-z][A-Za-z0-9-]*=?\d*\s+\S.*$`)
	rgLongOptRe       = regexp.MustCompile(`^--[A-Za-z][A-Za-z0-9-]*$`)
	rgShortClusterRe  = regexp.MustCompile(`^-[A-Za-z0-9]+$`)
)

func parseRgArgs(args []string) (*rgOptions, error) {
	o := &rgOptions{maxDepth: rgMaxDepth}
	endOfFlags := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if strings.TrimSpace(arg) == "" {
			continue
		}
		if endOfFlags {
			o.positionals = append(o.positionals, arg)
			continue
		}
		next := func(name string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("Option %s needs a value. %s", name, rgSupported)
			}
			i++
			return args[i], nil
		}
		switch {
		case arg == "--":
			endOfFlags = true

		case rgFlagWithValueRe.MatchString(arg):
			return nil, fmt.Errorf(`Argument '%s' holds a flag and its value in one element; pass each as its own array element, e.g. ["-A", "2", "pattern"]. If it is a pattern that starts with -, pass it as ["-e", "%s"] or put "--" before it.`, arg, arg)

		case strings.HasPrefix(arg, "--") && rgLongOptRe.MatchString(strings.SplitN(arg, "=", 2)[0]):
			parts := strings.SplitN(arg, "=", 2)
			name := parts[0]
			value, hasValue := "", len(parts) == 2
			if hasValue {
				value = parts[1]
			}
			takesValue := map[string]bool{"--glob": true, "--include": true, "--regexp": true, "--context": true,
				"--before-context": true, "--after-context": true, "--max-depth": true, "--maxdepth": true,
				"--color": true, "--colour": true}
			if takesValue[name] && !hasValue {
				v, err := next(name)
				if err != nil {
					return nil, err
				}
				value = v
			}
			if err := o.applyLong(name, value); err != nil {
				return nil, err
			}

		case rgShortClusterRe.MatchString(arg):
			cluster := arg[1:]
			for j := 0; j < len(cluster); j++ {
				c := cluster[j]
				if strings.IndexByte("geABCd", c) >= 0 {
					value := cluster[j+1:]
					if value == "" {
						if i+1 >= len(args) {
							return nil, fmt.Errorf("Flag -%c needs a value. %s", c, rgSupported)
						}
						i++
						value = args[i]
					}
					if err := o.applyValue("-"+string(c), value); err != nil {
						return nil, err
					}
					break
				}
				switch c {
				case 'i':
					o.ignoreCase = true
				case 'F':
					o.fixed = true
				case 'l':
					o.filesWithMatches = true
				case 'o':
					o.onlyMatching = true
				case 'n', 'H', 'r', 'R', 'E':
				default:
					in := ""
					if len(arg) > 2 {
						in = " in " + arg
					}
					return nil, fmt.Errorf("Unsupported flag -%c%s. %s", c, in, rgSupported)
				}
			}

		case len(arg) > 2 && arg[0] == '-' && isASCIILetter(arg[1]):
			return nil, fmt.Errorf(`Cannot read '%s' as flags; pass a flag and its value as separate elements, e.g. ["-g", "*.java"]. If it is a pattern that starts with -, pass it as ["-e", "%s"] or put "--" before it. %s`, arg, arg, rgSupported)

		default:
			o.positionals = append(o.positionals, arg)
		}
	}
	return o, nil
}

func isASCIILetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }

func (o *rgOptions) applyLong(name, value string) error {
	switch name {
	case "--files":
		o.listFiles = true
	case "--ignore-case":
		o.ignoreCase = true
	case "--fixed-strings":
		o.fixed = true
	case "--files-with-matches":
		o.filesWithMatches = true
	case "--only-matching":
		o.onlyMatching = true
	case "--glob", "--include":
		return o.applyValue("-g", value)
	case "--regexp":
		return o.applyValue("-e", value)
	case "--context":
		return o.applyValue("-C", value)
	case "--before-context":
		return o.applyValue("-B", value)
	case "--after-context":
		return o.applyValue("-A", value)
	case "--max-depth", "--maxdepth":
		return o.applyValue("-d", value)
	case "--color", "--colour":
	case "--line-number", "--with-filename", "--no-heading", "--heading", "--recursive", "--extended-regexp":
	default:
		return fmt.Errorf(`Unsupported option %s. To search for it, pass ["-e", "%s"]. %s`, name, name, rgSupported)
	}
	return nil
}

func (o *rgOptions) applyValue(flag, value string) error {
	switch flag {
	case "-g", "-e":
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("Flag %s needs a non-empty value. %s", flag, rgSupported)
		}
		if flag == "-g" {
			o.globs = append(o.globs, value)
		} else {
			o.patterns = append(o.patterns, value)
		}
	case "-A", "-B", "-C":
		n, ok := clampDigits(value, rgMaxContextLines)
		if !ok {
			return fmt.Errorf("%s needs a number of lines, got '%s'. %s", flag, value, rgSupported)
		}
		switch flag {
		case "-A":
			o.after, o.afterSet = n, true
		case "-B":
			o.before, o.beforeSet = n, true
		default:
			o.contextLines = n
		}
	case "-d":
		n, ok := clampDigits(value, rgMaxDepth)
		if !ok {
			return fmt.Errorf("%s needs a non-negative number, got '%s'. %s", flag, value, rgSupported)
		}
		o.maxDepth = n
	}
	return nil
}

func clampDigits(v string, maxV int) (int, bool) {
	if v == "" {
		return 0, false
	}
	for i := 0; i < len(v); i++ {
		if v[i] < '0' || v[i] > '9' {
			return 0, false
		}
	}
	if len(v) > 4 {
		return maxV, true
	}
	n, _ := strconv.Atoi(v)
	return min(n, maxV), true
}

// globToRegexp converts a ripgrep-style glob: **/ is any depth (including
// none), * and ** are .*, ? is one character, {a,b} is alternation.
func globToRegexp(glob string, ignoreCase bool) *regexp.Regexp {
	var sb strings.Builder
	if ignoreCase {
		sb.WriteString("(?i)")
	}
	sb.WriteString("^")
	depth := 0
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case strings.HasPrefix(glob[i:], "**/"):
			sb.WriteString("(?:.*/)?")
			i += 2
		case c == '*':
			sb.WriteString(".*")
			for i+1 < len(glob) && glob[i+1] == '*' {
				i++
			}
		case c == '?':
			sb.WriteString(".")
		case c == '{':
			sb.WriteString("(?:")
			depth++
		case c == '}' && depth > 0:
			sb.WriteString(")")
			depth--
		case c == ',' && depth > 0:
			sb.WriteString("|")
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	for ; depth > 0; depth-- {
		sb.WriteString(")")
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return regexp.MustCompile("^" + regexp.QuoteMeta(glob) + "$")
	}
	return re
}

// MatchesGlob reports whether a repository-relative path matches a
// ripgrep-style glob: a glob with "/" matches the whole path (** spans
// directories), one without matches the file name.
func MatchesGlob(glob, path string) bool {
	glob = strings.TrimSpace(glob)
	if glob == "" {
		return false
	}
	m := globMatcher{re: globToRegexp(glob, false), fullPath: strings.Contains(glob, "/")}
	if m.fullPath {
		return m.re.MatchString(path)
	}
	return m.re.MatchString(filepath.Base(path))
}

type rgGlobFilter struct {
	include, exclude []globMatcher
}

type globMatcher struct {
	re       *regexp.Regexp
	fullPath bool // glob contains "/": match the relative path, else the file name
}

func newGlobFilter(globs []string, ignoreCase bool) rgGlobFilter {
	var f rgGlobFilter
	for _, g := range globs {
		neg := strings.HasPrefix(g, "!")
		g = strings.TrimPrefix(g, "!")
		m := globMatcher{re: globToRegexp(g, ignoreCase), fullPath: strings.Contains(g, "/")}
		if neg {
			f.exclude = append(f.exclude, m)
		} else {
			f.include = append(f.include, m)
		}
	}
	return f
}

func (f rgGlobFilter) allows(rel string) bool {
	match := func(m globMatcher) bool {
		if m.fullPath {
			return m.re.MatchString(rel)
		}
		return m.re.MatchString(filepath.Base(rel))
	}
	for _, m := range f.exclude {
		if match(m) {
			return false
		}
	}
	if len(f.include) == 0 {
		return true
	}
	for _, m := range f.include {
		if match(m) {
			return true
		}
	}
	return false
}

// runRg executes the rg tool. It returns the tool output, or an error the
// model should see (bad arguments, no path found).
func (w *Workspace) runRg(args []string) (string, error) {
	o, err := parseRgArgs(args)
	if err != nil {
		return "", err
	}

	pattern := ""
	paths := o.positionals
	switch {
	case len(o.patterns) > 0:
		if len(o.patterns) == 1 {
			pattern = o.patterns[0]
			if o.fixed {
				pattern = regexp.QuoteMeta(pattern)
			}
		} else {
			var parts []string
			for _, p := range o.patterns {
				if o.fixed {
					parts = append(parts, regexp.QuoteMeta(p))
				} else {
					parts = append(parts, "(?:"+p+")")
				}
			}
			pattern = strings.Join(parts, "|")
		}
	case o.listFiles:
	default:
		if len(paths) == 0 {
			return "", fmt.Errorf("search tool requires at least a pattern argument")
		}
		pattern = paths[0]
		paths = paths[1:]
		if o.fixed {
			pattern = regexp.QuoteMeta(pattern)
		}
	}
	if len(paths) == 0 {
		paths = []string{"."}
	}

	files, notFoundNote, err := w.rgCollect(paths, o)
	if err != nil {
		return "", err
	}
	filter := newGlobFilter(o.globs, o.ignoreCase)

	if o.listFiles {
		var listed []string
		for _, f := range files {
			if filter.allows(f) {
				listed = append(listed, f)
			}
		}
		if len(listed) == 0 {
			return notFoundNote + "No files found.", nil
		}
		more := ""
		if len(listed) > rgMaxMatches {
			listed = listed[:rgMaxMatches]
			more = fmt.Sprintf("\n... (more than %d files; narrow it with a path or -g <glob>)", rgMaxMatches)
		}
		return truncateRg(notFoundNote + strings.Join(listed, "\n") + more), nil
	}

	displayPattern := pattern
	if len(o.patterns) > 0 {
		displayPattern = strings.Join(o.patterns, " | ")
	} else if o.fixed {
		displayPattern = o.positionals[0]
	}
	flags := ""
	if o.ignoreCase {
		flags = "(?i)"
	}
	literalNote := ""
	re, compileErr := regexp.Compile(flags + pattern)
	if compileErr != nil {
		re = regexp.MustCompile(flags + regexp.QuoteMeta(pattern))
		literalNote = fmt.Sprintf("Note: '%s' is not a valid regex (%v), so it was searched as a literal string. Escape special characters (e.g. \\() or use -F.\n", pattern, compileErr)
	}

	perFileLimit := rgMaxMatchesPerFile
	if len(paths) == 1 {
		if abs, err := w.Resolve(paths[0]); err == nil {
			if st, err := os.Stat(abs); err == nil && st.Mode().IsRegular() {
				perFileLimit = 0 // unlimited
			}
		}
	}

	lines, capped := w.rgSearch(files, filter, re, o, perFileLimit)
	if len(lines) == 0 && !o.fixed && strings.Contains(pattern, `\|`) && compileErr == nil {
		alt := strings.ReplaceAll(pattern, `\|`, "|")
		if re2, err := regexp.Compile(flags + alt); err == nil {
			if l2, c2 := w.rgSearch(files, filter, re2, o, perFileLimit); len(l2) > 0 {
				lines, capped, displayPattern = l2, c2, strings.ReplaceAll(displayPattern, `\|`, "|")
			}
		}
	}

	out := literalNote + notFoundNote
	if capped {
		out += rgCappedNote + "\n"
	}
	if len(lines) == 0 {
		out += "No matches found for pattern: " + displayPattern
	} else {
		out += strings.Join(lines, "\n")
	}
	return truncateRg(out), nil
}

func truncateRg(s string) string {
	if len(s) > rgMaxOutputChars {
		return s[:rgMaxOutputChars] + "\n... (output truncated)"
	}
	return s
}

// rgCollect walks the given paths and returns the workspace-relative regular
// files, sorted and de-duplicated, plus a note naming paths that were not found.
func (w *Workspace) rgCollect(paths []string, o *rgOptions) ([]string, string, error) {
	var notFound []string
	seen := map[string]bool{}
	var files []string
	found := 0
	for _, p := range paths {
		abs, err := w.Resolve(p)
		if err != nil {
			if len(paths) == 1 {
				return nil, "", err
			}
			notFound = append(notFound, fmt.Sprintf("%s (%v)", p, err))
			continue
		}
		st, err := os.Lstat(abs)
		if err != nil {
			notFound = append(notFound, p)
			continue
		}
		found++
		add := func(path string) {
			rel, err := filepath.Rel(w.Dir, path)
			if err != nil || hasGitSegment(rel) || seen[rel] {
				return
			}
			seen[rel] = true
			files = append(files, filepath.ToSlash(rel))
		}
		if st.Mode().IsRegular() {
			add(abs)
			continue
		}
		if !st.IsDir() || o.maxDepth == 0 {
			continue
		}
		filepath.WalkDir(abs, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			rel, _ := filepath.Rel(abs, path)
			depth := 0
			if rel != "." {
				depth = strings.Count(filepath.ToSlash(rel), "/") + 1
			}
			if d.IsDir() {
				if path != abs && (strings.EqualFold(d.Name(), ".git") || depth >= o.maxDepth) {
					return filepath.SkipDir
				}
				return nil
			}
			if d.Type().IsRegular() && depth <= o.maxDepth {
				add(path)
			}
			return nil
		})
	}
	if found == 0 {
		return nil, "", fmt.Errorf("Path not found: %s", strings.Join(notFound, ", "))
	}
	sort.Strings(files)
	note := ""
	if len(notFound) > 0 {
		note = fmt.Sprintf("Not searched, path not found: %s\n", strings.Join(notFound, ", "))
	}
	return files, note, nil
}

func hasGitSegment(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if strings.EqualFold(seg, ".git") {
			return true
		}
	}
	return false
}

var rgLineSplit = regexp.MustCompile(`\r\n|\n|\r`)

// readTextFile returns the file's lines, or ok=false for files to skip
// silently (too large, binary, unreadable). Invalid UTF-8 is replaced.
func (w *Workspace) readTextFile(rel string) ([]string, bool) {
	abs := filepath.Join(w.Dir, filepath.FromSlash(rel))
	st, err := os.Stat(abs)
	if err != nil || st.Size() > rgMaxFileSize {
		return nil, false
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, false
	}
	if bytes.IndexByte(data[:min(len(data), rgBinarySniffBytes)], 0) >= 0 {
		return nil, false
	}
	text := strings.ToValidUTF8(string(data), "�")
	text = strings.TrimSuffix(strings.TrimSuffix(text, "\n"), "\r")
	if text == "" {
		return nil, true
	}
	return rgLineSplit.Split(text, -1), true
}

func (w *Workspace) rgSearch(files []string, filter rgGlobFilter, re *regexp.Regexp, o *rgOptions, perFileLimit int) ([]string, bool) {
	before, after := o.contextLines, o.contextLines
	if o.beforeSet {
		before = o.before
	}
	if o.afterSet {
		after = o.after
	}
	withContext := before > 0 || after > 0

	var out []string
	reported := 0
	capped := false
	lastFileWithOutput := ""
	for _, rel := range files {
		if capped {
			break
		}
		if !filter.allows(rel) {
			continue
		}
		lines, ok := w.readTextFile(rel)
		if !ok || len(lines) == 0 {
			continue
		}

		if o.filesWithMatches {
			for _, l := range lines {
				if re.MatchString(l) {
					if reported >= rgMaxMatches {
						capped = true
					} else {
						out = append(out, rel)
						reported++
					}
					break
				}
			}
			continue
		}

		var hits []int
		more := 0
		for i, l := range lines {
			if !re.MatchString(l) {
				continue
			}
			if perFileLimit > 0 && len(hits) >= perFileLimit {
				more++
				continue
			}
			if reported >= rgMaxMatches {
				capped = true
				break
			}
			hits = append(hits, i)
			reported++
		}
		if len(hits) == 0 && more == 0 {
			continue
		}

		printed := map[int]bool{}
		lastPrinted := -2
		emit := func(i int, hit bool) {
			if printed[i] {
				return
			}
			if withContext && len(out) > 0 && (i != lastPrinted+1 || lastFileWithOutput != rel) {
				out = append(out, "--")
			}
			printed[i] = true
			lastPrinted = i
			lastFileWithOutput = rel
			if !hit {
				out = append(out, fmt.Sprintf("%s-%d- %s", rel, i+1, lines[i]))
				return
			}
			if o.onlyMatching {
				for _, m := range re.FindAllString(lines[i], -1) {
					if m != "" {
						out = append(out, fmt.Sprintf("%s:%d: %s", rel, i+1, m))
					}
				}
				return
			}
			out = append(out, fmt.Sprintf("%s:%d: %s", rel, i+1, lines[i]))
		}
		isHit := map[int]bool{}
		for _, h := range hits {
			isHit[h] = true
		}
		for _, h := range hits {
			for i := max(0, h-before); i < h; i++ {
				emit(i, isHit[i])
			}
			emit(h, true)
			for i := h + 1; i <= min(len(lines)-1, h+after); i++ {
				emit(i, isHit[i])
			}
		}
		if more > 0 {
			word := "matches"
			if more == 1 {
				word = "match"
			}
			out = append(out, fmt.Sprintf("%s: … %d more %s in this file; search that file alone to see all.", rel, more, word))
			reported++
		}
	}
	return out, capped
}
