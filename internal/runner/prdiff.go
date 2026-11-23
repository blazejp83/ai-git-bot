package runner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxPRDiffPaths = 8
	maxPRDiffChars = 40000
)

// prDiffFile is one file's section of a unified git diff.
type prDiffFile struct {
	path  string // path in the new tree (old path for deletions)
	block string // text after the "diff --git" line, up to the next one
}

// parsePRDiff splits a unified git diff into per-file blocks.
func parsePRDiff(diff string) []prDiffFile {
	var files []prDiffFile
	var cur *strings.Builder
	var header string
	flush := func() {
		if cur != nil {
			files = append(files, prDiffFile{path: diffBlockPath(header, cur.String()), block: cur.String()})
		}
	}
	for _, line := range strings.SplitAfter(diff, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			header = strings.TrimRight(line, "\r\n")
			cur = &strings.Builder{}
			continue
		}
		if cur != nil {
			cur.WriteString(line)
		}
	}
	flush()
	return files
}

// diffBlockPath picks the file's path: the "+++ b/" path, else the "---" path
// for a deletion, else the "rename to" path, else the path in the header.
func diffBlockPath(header, block string) string {
	var plus, minus, renameTo string
	for _, l := range strings.Split(block, "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case strings.HasPrefix(l, "+++ "):
			plus = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
		case strings.HasPrefix(l, "--- "):
			minus = strings.TrimPrefix(strings.TrimPrefix(l, "--- "), "a/")
		case strings.HasPrefix(l, "rename to "):
			renameTo = strings.TrimPrefix(l, "rename to ")
		case strings.HasPrefix(l, "@@"):
			goto done
		}
	}
done:
	if plus != "" && plus != "/dev/null" {
		return plus
	}
	if plus == "/dev/null" && minus != "" {
		return minus
	}
	if renameTo != "" {
		return renameTo
	}
	// "diff --git a/x b/x"
	if i := strings.LastIndex(header, " b/"); i >= 0 {
		return header[i+3:]
	}
	return ""
}

func isBinaryDiffBlock(block string) bool {
	for _, l := range strings.Split(block, "\n") {
		if strings.HasPrefix(l, "@@") {
			return false
		}
		if strings.HasPrefix(l, "Binary files ") || strings.HasPrefix(l, "GIT binary patch") {
			return true
		}
	}
	return false
}

func isNewFileDiffBlock(block string) bool {
	for _, l := range strings.Split(block, "\n") {
		l = strings.TrimRight(l, "\r")
		if strings.HasPrefix(l, "@@") {
			return false
		}
		if strings.HasPrefix(l, "new file mode") || l == "--- /dev/null" {
			return true
		}
	}
	return false
}

var hunkHeaderRe = regexp.MustCompile(`^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@`)

// numberDiffBlock prefixes added and context lines with their line number in
// the new file ("37 | +code"); removed lines get blank padding.
func numberDiffBlock(block string) string {
	lines := strings.Split(block, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // trailing newline
	}
	maxLine := 1
	for _, l := range lines {
		if m := hunkHeaderRe.FindStringSubmatch(l); m != nil {
			start, _ := strconv.Atoi(m[1])
			count := 1
			if m[2] != "" {
				count, _ = strconv.Atoi(m[2])
			}
			last := start + max(count-1, 0)
			maxLine = max(maxLine, last)
		}
	}
	width := len(strconv.Itoa(maxLine))
	pad := strings.Repeat(" ", width) + " | "

	var sb strings.Builder
	inHunk := false
	n := 0
	for _, l := range lines {
		if m := hunkHeaderRe.FindStringSubmatch(l); m != nil {
			inHunk = true
			n, _ = strconv.Atoi(m[1])
			sb.WriteString(l + "\n")
			continue
		}
		if !inHunk {
			sb.WriteString(l + "\n")
			continue
		}
		if l == "" || strings.HasPrefix(l, "+") || strings.HasPrefix(l, " ") {
			fmt.Fprintf(&sb, "%*d | %s\n", width, n, l)
			n++
		} else {
			sb.WriteString(pad + l + "\n")
		}
	}
	return sb.String()
}

// prDiffBlock returns the numbered block for one path, or "" if the path has
// no hunks in the diff.
func prDiffBlock(files []prDiffFile, path string) (string, bool) {
	for _, f := range files {
		if f.path == path {
			if isBinaryDiffBlock(f.block) {
				return "Binary file changed\n", false
			}
			return numberDiffBlock(f.block), isNewFileDiffBlock(f.block)
		}
	}
	return "", false
}

// renderPRDiff implements the pr_diff tool: up to 8 paths per call, about
// 40000 characters per call (the first file shown is never cut), notes first.
func renderPRDiff(diff string, paths []string) (string, error) {
	var clean []string
	seen := map[string]bool{}
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return "", fmt.Errorf("pr_diff requires a file path argument")
	}
	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("no PR diff available in this context")
	}
	files := parsePRDiff(diff)

	shown := clean
	var deferred, missing []string
	if len(shown) > maxPRDiffPaths {
		deferred = append(deferred, shown[maxPRDiffPaths:]...)
		shown = shown[:maxPRDiffPaths]
	}

	var hunks strings.Builder
	shownCount := 0
	for _, p := range shown {
		block, isNew := prDiffBlock(files, p)
		if strings.TrimSpace(block) == "" {
			missing = append(missing, p)
			continue
		}
		if isNew {
			block = "(new file: this diff is the complete file content; no need to read it)\n" + block
		}
		heading := ""
		if len(clean) > 1 {
			if shownCount > 0 {
				heading = "\n\n"
			}
			heading += "### " + p + "\n"
		}
		block = strings.TrimRight(block, "\r\n")
		if hunks.Len() > 0 && hunks.Len()+len(heading)+len(block) > maxPRDiffChars {
			deferred = append(deferred, p)
			continue
		}
		hunks.WriteString(heading + block)
		shownCount++
	}

	var notes strings.Builder
	if len(missing) > 0 {
		fmt.Fprintf(&notes, "No diff hunks found for: %s\n", strings.Join(missing, ", "))
		if hunks.Len() == 0 {
			var all []string
			for _, f := range files {
				all = append(all, f.path)
			}
			fmt.Fprintf(&notes, "Changed files: %s\n", strings.Join(all, ", "))
		}
	}
	if len(deferred) > 0 {
		fmt.Fprintf(&notes, "Not shown, call pr_diff again for: %s (at most %d files and about %d characters per call)\n",
			strings.Join(deferred, ", "), maxPRDiffPaths, maxPRDiffChars)
	}
	out := notes.String()
	if out != "" && hunks.Len() > 0 {
		out += "\n"
	}
	return strings.TrimRight(out+hunks.String(), "\r\n"), nil
}
