package agent

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

var diffBlockPattern = regexp.MustCompile(`(?s)<<<<<<< SEARCH\s*\n(.*?)=======\s*\n(.*?)>>>>>>> REPLACE`)

type searchReplace struct {
	search  string
	replace string
}

// ErrDiffApply is returned when a search block cannot be found in the content.
var ErrDiffApply = errors.New("diff apply failed")

// ApplyDiff applies SEARCH/REPLACE blocks from a diff to the original content.
func ApplyDiff(original, diff string) (string, error) {
	if diff == "" {
		return original, nil
	}

	blocks := parseDiffBlocks(diff)
	if len(blocks) == 0 {
		slog.Warn("No valid SEARCH/REPLACE blocks found in diff")
		return original, nil
	}

	result := original
	for _, block := range blocks {
		var err error
		result, err = applyBlock(result, block)
		if err != nil {
			return "", err
		}
	}
	return result, nil
}

func parseDiffBlocks(diff string) []searchReplace {
	matches := diffBlockPattern.FindAllStringSubmatch(diff, -1)
	var blocks []searchReplace
	for _, m := range matches {
		search := strings.TrimSuffix(m[1], "\n")
		replace := strings.TrimSuffix(m[2], "\n")
		blocks = append(blocks, searchReplace{search: search, replace: replace})
	}
	return blocks
}

func applyBlock(content string, block searchReplace) (string, error) {
	search := block.search
	replace := block.replace

	// Empty search = append to end
	if strings.TrimSpace(search) == "" {
		if strings.TrimSpace(content) == "" {
			return replace, nil
		}
		return content + "\n" + replace, nil
	}

	// Placeholder comment detection
	if isPlaceholderComment(search) && !strings.Contains(content, search) {
		if strings.TrimSpace(content) == "" {
			return replace, nil
		}
		return content + "\n" + replace, nil
	}

	// Strategy 1: Exact match
	if strings.Contains(content, search) {
		return strings.Replace(content, search, replace, 1), nil
	}

	// Strategy 2: Normalize line endings
	norm := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n") }
	nc := norm(content)
	ns := norm(search)
	if strings.Contains(nc, ns) {
		return strings.Replace(nc, ns, replace, 1), nil
	}

	// Strategy 3: Trim trailing whitespace per line
	trimTrailing := func(s string) string {
		lines := strings.Split(s, "\n")
		for i := range lines {
			lines[i] = strings.TrimRight(lines[i], " \t")
		}
		return strings.Join(lines, "\n")
	}
	tc := trimTrailing(nc)
	ts := trimTrailing(ns)
	if strings.Contains(tc, ts) {
		return strings.Replace(nc, ns, replace, 1), nil
	}

	// Strategy 4: Fuzzy line-by-line (trim each line fully)
	if result, ok := fuzzyMatch(nc, ns, replace); ok {
		return result, nil
	}

	// Strategy 5: Collapsed empty lines
	if result, ok := collapsedMatch(nc, ns, replace); ok {
		return result, nil
	}

	// Strategy 6: Append pattern
	if isAppendPattern(search, replace) {
		newContent := extractAppendContent(search, replace)
		if strings.TrimSpace(content) == "" {
			return newContent, nil
		}
		return content + "\n" + newContent, nil
	}

	// Strategy 7: Similarity-based (Levenshtein)
	if result, ok := similarityMatch(nc, ns, replace); ok {
		return result, nil
	}

	preview := search
	if len(preview) > 200 {
		preview = preview[:200] + "..."
	}
	return "", fmt.Errorf("%w: search block not found in file content.\n\nExpected to find:\n```\n%s\n```", ErrDiffApply, preview)
}

func fuzzyMatch(content, search, replace string) (string, bool) {
	contentLines := strings.Split(content, "\n")
	searchLines := strings.Split(search, "\n")
	trimmedSearch := make([]string, len(searchLines))
	for i, l := range searchLines {
		trimmedSearch[i] = strings.TrimSpace(l)
	}

	for i := 0; i <= len(contentLines)-len(searchLines); i++ {
		matches := true
		for j := 0; j < len(searchLines); j++ {
			if strings.TrimSpace(contentLines[i+j]) != trimmedSearch[j] {
				matches = false
				break
			}
		}
		if matches {
			return applyFuzzyReplace(contentLines, i, len(searchLines), replace), true
		}
	}
	return "", false
}

func collapsedMatch(content, search, replace string) (string, bool) {
	contentLines := strings.Split(content, "\n")
	var nonEmptySearch []string
	for _, l := range strings.Split(search, "\n") {
		if strings.TrimSpace(l) != "" {
			nonEmptySearch = append(nonEmptySearch, strings.TrimSpace(l))
		}
	}
	if len(nonEmptySearch) == 0 {
		return "", false
	}

	for i := 0; i < len(contentLines); i++ {
		si := 0
		startLine := -1
		endLine := -1

		for j := i; j < len(contentLines) && si < len(nonEmptySearch); j++ {
			trimmed := strings.TrimSpace(contentLines[j])
			if trimmed == "" {
				continue
			}
			if trimmed == nonEmptySearch[si] {
				if startLine == -1 {
					startLine = j
				}
				endLine = j
				si++
			} else {
				break
			}
		}

		if si == len(nonEmptySearch) && startLine != -1 {
			return applyFuzzyReplace(contentLines, startLine, endLine-startLine+1, replace), true
		}
	}
	return "", false
}

func similarityMatch(content, search, replace string) (string, bool) {
	contentLines := strings.Split(content, "\n")
	var nonEmptySearch []string
	for _, l := range strings.Split(search, "\n") {
		if strings.TrimSpace(l) != "" {
			nonEmptySearch = append(nonEmptySearch, strings.TrimSpace(l))
		}
	}
	if len(nonEmptySearch) < 3 {
		return "", false
	}

	for i := 0; i < len(contentLines); i++ {
		si := 0
		startLine := -1
		endLine := -1
		mismatches := 0

		for j := i; j < len(contentLines) && si < len(nonEmptySearch); j++ {
			trimmed := strings.TrimSpace(contentLines[j])
			if trimmed == "" {
				continue
			}
			if linesSimilar(trimmed, nonEmptySearch[si]) {
				if startLine == -1 {
					startLine = j
				}
				endLine = j
				if trimmed != nonEmptySearch[si] {
					mismatches++
				}
				si++
			} else {
				break
			}
		}

		maxMismatches := max(2, len(nonEmptySearch)*3/10)
		if si == len(nonEmptySearch) && startLine != -1 && mismatches <= maxMismatches {
			slog.Info("Applied diff using similarity matching", "start_line", startLine)
			return applyFuzzyReplace(contentLines, startLine, endLine-startLine+1, replace), true
		}
	}
	return "", false
}

func applyFuzzyReplace(lines []string, startLine, lineCount int, replace string) string {
	var sb strings.Builder
	for i := 0; i < startLine; i++ {
		sb.WriteString(lines[i])
		sb.WriteByte('\n')
	}
	sb.WriteString(replace)
	afterMatch := startLine + lineCount
	if afterMatch < len(lines) {
		sb.WriteByte('\n')
		for i := afterMatch; i < len(lines); i++ {
			sb.WriteString(lines[i])
			if i < len(lines)-1 {
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}

func linesSimilar(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) < 10 || len(b) < 10 {
		return false
	}
	dist := levenshtein(a, b)
	maxLen := max(len(a), len(b))
	threshold := min(15, max(3, maxLen/10))
	return dist <= threshold
}

func levenshtein(s1, s2 string) int {
	m, n := len(s1), len(s2)
	prev := make([]int, n+1)
	curr := make([]int, n+1)
	for j := 0; j <= n; j++ {
		prev[j] = j
	}
	for i := 1; i <= m; i++ {
		curr[0] = i
		for j := 1; j <= n; j++ {
			cost := 1
			if s1[i-1] == s2[j-1] {
				cost = 0
			}
			curr[j] = min(min(curr[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}
	return prev[n]
}

func isPlaceholderComment(search string) bool {
	trimmed := strings.TrimSpace(search)
	var inner string
	switch {
	case strings.HasPrefix(trimmed, "/*") && strings.HasSuffix(trimmed, "*/"):
		inner = strings.ToLower(trimmed[2 : len(trimmed)-2])
	case strings.HasPrefix(trimmed, "//"):
		inner = strings.ToLower(trimmed[2:])
	case strings.HasPrefix(trimmed, "#"):
		inner = strings.ToLower(trimmed[1:])
	case strings.HasPrefix(trimmed, "<!--") && strings.HasSuffix(trimmed, "-->"):
		inner = strings.ToLower(trimmed[4 : len(trimmed)-3])
	default:
		return false
	}
	keywords := []string{"existing", "placeholder", "add", "content here", "your code", "rest of"}
	for _, kw := range keywords {
		if strings.Contains(inner, kw) {
			return true
		}
	}
	return false
}

func isAppendPattern(search, replace string) bool {
	if search == "" || replace == "" {
		return false
	}
	normWS := func(s string) string {
		lines := strings.Split(s, "\n")
		for i := range lines {
			lines[i] = strings.TrimSpace(lines[i])
		}
		return strings.Join(lines, "\n")
	}
	ns := normWS(search)
	nr := normWS(replace)
	return strings.HasPrefix(nr, ns) && len(nr) > len(ns)
}

func extractAppendContent(search, replace string) string {
	searchLineCount := len(strings.Split(search, "\n"))
	replaceLines := strings.Split(replace, "\n")
	if len(replaceLines) > searchLineCount {
		return strings.Join(replaceLines[searchLineCount:], "\n")
	}
	return replace
}
