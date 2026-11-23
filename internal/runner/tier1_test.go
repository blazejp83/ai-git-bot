package runner

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tmseidel/ai-git-bot/internal/ai"
)

// --- pr_diff ---

const sampleDiff = `diff --git a/src/Svc.java b/src/Svc.java
index 111..222 100644
--- a/src/Svc.java
+++ b/src/Svc.java
@@ -8,3 +8,4 @@ class Svc {
 keep
-old
+new1
+new2
 tail
@@ -98,2 +99,2 @@
-x
+y
\ No newline at end of file
diff --git a/src/New.java b/src/New.java
new file mode 100644
index 000..333
--- /dev/null
+++ b/src/New.java
@@ -0,0 +1,2 @@
+class New {}
+// end
diff --git a/img.png b/img.png
index 1..2 100644
Binary files a/img.png and b/img.png differ
`

func TestPRDiffNumbersLines(t *testing.T) {
	out, err := renderPRDiff(sampleDiff, []string{"src/Svc.java"})
	if err != nil {
		t.Fatal(err)
	}
	want := `@@ -8,3 +8,4 @@ class Svc {
  8 |  keep
    | -old
  9 | +new1
 10 | +new2
 11 |  tail
@@ -98,2 +99,2 @@
    | -x
 99 | +y
    | \ No newline at end of file`
	if !strings.HasSuffix(out, want) {
		t.Errorf("numbered block mismatch:\n%s", out)
	}
	if strings.Contains(out, "###") {
		t.Errorf("single path must not get a heading:\n%s", out)
	}
}

func TestPRDiffNewMissingBinaryAndHeadings(t *testing.T) {
	out, err := renderPRDiff(sampleDiff, []string{"src/New.java", "src/Gone.java", "img.png", "src/New.java"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"No diff hunks found for: src/Gone.java\n\n### src/New.java\n",
		"(new file: this diff is the complete file content; no need to read it)",
		"1 | +class New {}",
		"\n\n### img.png\nBinary file changed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "Changed files:") {
		t.Errorf("Changed files list only when nothing was shown:\n%s", out)
	}

	out, _ = renderPRDiff(sampleDiff, []string{"nope"})
	if out != "No diff hunks found for: nope\nChanged files: src/Svc.java, src/New.java, img.png" {
		t.Errorf("all-missing output = %q", out)
	}
}

func TestPRDiffDefersBeyondEightPaths(t *testing.T) {
	paths := []string{"src/Svc.java", "a", "b", "c", "d", "e", "f", "g", "src/New.java"}
	out, _ := renderPRDiff(sampleDiff, paths)
	if !strings.Contains(out, "Not shown, call pr_diff again for: src/New.java") {
		t.Errorf("9th path not deferred:\n%s", out)
	}
	if _, err := renderPRDiff(sampleDiff, []string{" "}); err == nil {
		t.Error("blank paths must fail")
	}
}

// --- rg ---

func newTestWorkspace(t *testing.T, files map[string]string) *Workspace {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		abs := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(abs), 0o755)
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Workspace{Dir: dir}
}

func TestRgSearchFlagsAndOutput(t *testing.T) {
	ws := newTestWorkspace(t, map[string]string{
		"src/a.go":       "package a\nfunc Foo() {}\n// foo here\nfunc Bar() {}\n",
		"src/a_test.go":  "func TestFoo() {}\n",
		"docs/readme.md": "Foo docs\n",
		".git/config":    "Foo\n",
		"bin.dat":        "Foo\x00binary",
		"bad.txt":        "Foo \xff\xfe bytes\n",
	})

	out, err := ws.runRg([]string{"-i", "foo"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"src/a.go:2: func Foo() {}", "src/a.go:3: // foo here", "docs/readme.md:1: Foo docs", "bad.txt:1: Foo \uFFFD bytes"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Contains(out, ".git") || strings.Contains(out, "bin.dat") {
		t.Errorf(".git and binary files must be skipped:\n%s", out)
	}

	out, _ = ws.runRg([]string{"Foo", "-g", "*.go", "-g", "!*_test.go", "-l"})
	if out != "src/a.go" {
		t.Errorf("glob + -l = %q", out)
	}

	out, _ = ws.runRg([]string{"-C", "1", "Bar", "src/a.go"})
	if out != "src/a.go-3- // foo here\nsrc/a.go:4: func Bar() {}" {
		t.Errorf("context output = %q", out)
	}

	out, _ = ws.runRg([]string{"-o", `func \w+`, "src/a.go"})
	if !strings.Contains(out, "src/a.go:2: func Foo") {
		t.Errorf("-o output = %q", out)
	}

	out, _ = ws.runRg([]string{"--files", "src"})
	if out != "src/a.go\nsrc/a_test.go" {
		t.Errorf("--files = %q", out)
	}

	out, _ = ws.runRg([]string{`Foo\|Bar`, "src/a.go"})
	if !strings.Contains(out, "src/a.go:4: func Bar() {}") {
		t.Errorf(`\| retry failed: %q`, out)
	}

	out, _ = ws.runRg([]string{"Foo(", "src/a.go"})
	if !strings.HasPrefix(out, "Note: 'Foo(' is not a valid regex") {
		t.Errorf("literal fallback note missing: %q", out)
	}

	out, _ = ws.runRg([]string{"zzz", "src", "missing"})
	if !strings.HasPrefix(out, "Not searched, path not found: missing\n") || !strings.HasSuffix(out, "No matches found for pattern: zzz") {
		t.Errorf("not-found note = %q", out)
	}
}

func TestRgArgumentErrors(t *testing.T) {
	ws := newTestWorkspace(t, map[string]string{"a.txt": "x\n"})
	for args, want := range map[string]string{
		"-w|x":          "Unsupported flag -w",
		"--type|go|x":   "Unsupported option --type",
		"-A2 name|x":    "holds a flag and its value in one element",
		"-g=*.go|x":     "Cannot read '-g=*.go' as flags",
		"-C|abc|x":      "-C needs a number of lines",
		"x|missing-dir": "Path not found: missing-dir",
	} {
		_, err := ws.runRg(strings.Split(args, "|"))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", args, err, want)
		}
	}
	if _, err := ws.runRg(nil); err == nil {
		t.Error("no args must fail")
	}
}

func TestRgPerFileLimitAndCap(t *testing.T) {
	var many strings.Builder
	for i := 0; i < 8; i++ {
		many.WriteString("hit\n")
	}
	ws := newTestWorkspace(t, map[string]string{"a.txt": many.String(), "b.txt": "hit\n"})
	out, _ := ws.runRg([]string{"hit"})
	if !strings.Contains(out, "a.txt: … 3 more matches in this file; search that file alone to see all.") {
		t.Errorf("per-file note missing:\n%s", out)
	}
	out, _ = ws.runRg([]string{"hit", "a.txt"})
	if strings.Count(out, "a.txt:") != 8 {
		t.Errorf("single-file search must show all hits:\n%s", out)
	}
}

func TestRgAcceptsOldSearchInput(t *testing.T) {
	got := rgArgsFromInput(map[string]any{"pattern": "-foo", "path": "src"})
	if strings.Join(got, " ") != "-e -foo src" {
		t.Errorf("old search input = %v", got)
	}
}

// --- workspace PR head checkout ---

func TestCheckoutRefFetchesPRHead(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	origin := t.TempDir()
	run := func(dir string, args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run(origin, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(origin, "f.txt"), []byte("base\n"), 0o644)
	run(origin, "add", ".")
	run(origin, "commit", "-q", "-m", "base")
	run(origin, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(origin, "f.txt"), []byte("pr change\n"), 0o644)
	run(origin, "commit", "-q", "-am", "pr")
	prSHA := run(origin, "rev-parse", "HEAD")
	run(origin, "update-ref", "refs/pull/7/head", prSHA)
	run(origin, "checkout", "-q", "main")
	run(origin, "branch", "-q", "-D", "feature") // only the PR ref holds the commit, like a fork PR

	ws, err := NewWorkspace(t.Context(), "file://"+origin, "o", "r", "main")
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Cleanup()
	sha, err := ws.CheckoutRef(t.Context(), "origin", "refs/pull/7/head")
	if err != nil {
		t.Fatal(err)
	}
	if sha != prSHA {
		t.Errorf("checked out %s, want %s", sha, prSHA)
	}
	if c, _ := ws.ReadFile("f.txt"); c != "pr change\n" {
		t.Errorf("workspace content = %q", c)
	}
}

func TestRedactURL(t *testing.T) {
	got := redactURL("fatal: could not read from https://oauth2:secret@host/o/r.git and http://u:p@x/y")
	if strings.Contains(got, "secret") || strings.Contains(got, "u:p") || !strings.Contains(got, "https://***@host/o/r.git") {
		t.Errorf("redactURL = %q", got)
	}
}

// --- compaction and tool-call pairing ---

func bigConversation(rounds int) []ai.ConversationMessage {
	msgs := []ai.ConversationMessage{{Role: "user", Content: "Implement issue #5: add a cache"}}
	big := strings.Repeat("x", 30000)
	for i := 0; i < rounds; i++ {
		id := "call_" + string(rune('a'+i))
		tc := ai.ToolCall{ID: id, Name: "read_file", Input: map[string]any{"path": "f.go"}}
		if i == 0 {
			tc = ai.ToolCall{ID: id, Name: "write_file", Input: map[string]any{"path": "cache.go", "content": "package c"}}
		}
		if i == 1 {
			tc = ai.ToolCall{ID: id, Name: "write_file", Input: map[string]any{"path": "broken.go"}}
		}
		msgs = append(msgs,
			ai.ConversationMessage{Role: "assistant", ToolCalls: []ai.ToolCall{tc}, Thinking: []ai.ThinkingBlock{{Type: "thinking", Signature: "s"}}},
			ai.ConversationMessage{Role: "user", ToolResults: []ai.ToolResultMessage{{ToolCallID: id, Content: big, IsError: i == 1}}},
		)
	}
	return msgs
}

func TestCompactMessages(t *testing.T) {
	if _, ok := compactMessages(bigConversation(2), ModeImplementation, "t"); ok {
		t.Fatal("small conversation must not be compacted")
	}
	msgs := bigConversation(10)
	out, ok := compactMessages(msgs, ModeImplementation, msgs[0].Content)
	if !ok {
		t.Fatal("large conversation not compacted")
	}
	sum := out[0]
	if sum.Role != "user" || !isCompactionSummary(sum.Content) {
		t.Fatalf("first message is not the summary: %+v", sum)
	}
	for _, want := range []string{"coding agent session", "Original task:\nImplement issue #5: add a cache", "Files edited earlier in this session: cache.go\n", "edits are already in the workspace"} {
		if !strings.Contains(sum.Content, want) {
			t.Errorf("summary missing %q:\n%s", want, sum.Content)
		}
	}
	if strings.Contains(sum.Content, "broken.go") {
		t.Error("a failed edit must not be listed")
	}
	if out[1].Role != "assistant" {
		t.Errorf("kept window must start on an assistant turn, got %s", out[1].Role)
	}
	for _, m := range out[1:] {
		if len(m.Thinking) > 0 {
			t.Error("kept turns must not carry thinking blocks")
		}
	}
	if len(out) != compactKeepMessages+1 {
		t.Errorf("len = %d, want %d", len(out), compactKeepMessages+1)
	}
	// Original messages untouched (the session keeps the full history).
	if len(msgs[len(msgs)-2].Thinking) == 0 {
		t.Error("compaction must not modify the input slice's messages")
	}
}

func TestPairToolCalls(t *testing.T) {
	msgs := []ai.ConversationMessage{
		{Role: "user", ToolResults: []ai.ToolResultMessage{{ToolCallID: "orphan"}}},
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []ai.ToolCall{{ID: "a", Name: "read_file"}, {ID: "b", Name: "done"}}},
		{Role: "user", ToolResults: []ai.ToolResultMessage{{ToolCallID: "a", Content: "ok"}, {ToolCallID: "zzz"}}},
		{Role: "assistant", ToolCalls: []ai.ToolCall{{ID: "c", Name: "read_file"}}},
	}
	out := pairToolCalls(msgs)
	if len(out) != 5 || out[0].Content != "go" {
		t.Fatalf("unexpected shape: %+v", out)
	}
	r1 := out[2].ToolResults
	if len(r1) != 2 || r1[0].ToolCallID != "a" || r1[1].ToolCallID != "b" || !strings.HasPrefix(r1[1].Content, "not executed") {
		t.Errorf("round 1 results = %+v", r1)
	}
	if r2 := out[4].ToolResults; len(r2) != 1 || r2[0].ToolCallID != "c" {
		t.Errorf("trailing call not answered: %+v", r2)
	}
}

func TestToolDefsAreSorted(t *testing.T) {
	r := NewToolRegistry(ModeImplementation, nil, 0, "diff --git a/x b/x\n")
	for i := 0; i < 5; i++ {
		defs := r.GetToolDefs()
		for j := 1; j < len(defs); j++ {
			if defs[j-1].Name >= defs[j].Name {
				t.Fatalf("tool defs not sorted: %s before %s", defs[j-1].Name, defs[j].Name)
			}
		}
	}
}
