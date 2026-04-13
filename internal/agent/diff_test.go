package agent

import (
	"strings"
	"testing"
)

func TestApplyDiff_SingleBlock(t *testing.T) {
	original := "public class Test {\n    public void hello() {\n        System.out.println(\"Hello\");\n    }\n}"
	diff := "<<<<<<< SEARCH\n        System.out.println(\"Hello\");\n=======\n        System.out.println(\"Hello, World!\");\n>>>>>>> REPLACE"

	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "Hello, World!") {
		t.Fatal("Should contain replacement")
	}
	if strings.Contains(result, `"Hello"`) && !strings.Contains(result, "World") {
		t.Fatal("Should not contain original")
	}
}

func TestApplyDiff_MultipleBlocks(t *testing.T) {
	original := "public class Test {\n    private int x = 1;\n    private int y = 2;\n}"
	diff := "<<<<<<< SEARCH\n    private int x = 1;\n=======\n    private int x = 10;\n>>>>>>> REPLACE\n\n<<<<<<< SEARCH\n    private int y = 2;\n=======\n    private int y = 20;\n>>>>>>> REPLACE"

	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "x = 10") || !strings.Contains(result, "y = 20") {
		t.Fatal("Both replacements should be applied")
	}
}

func TestApplyDiff_EmptyDiff(t *testing.T) {
	result, err := ApplyDiff("original", "")
	if err != nil {
		t.Fatal(err)
	}
	if result != "original" {
		t.Fatalf("got %q, want %q", result, "original")
	}
}

func TestApplyDiff_SearchNotFound(t *testing.T) {
	diff := "<<<<<<< SEARCH\nnot found text\n=======\nreplacement\n>>>>>>> REPLACE"
	_, err := ApplyDiff("some content", diff)
	if err == nil {
		t.Fatal("Should return error when search not found")
	}
}

func TestApplyDiff_DeleteLines(t *testing.T) {
	original := "line1\nline2\nline3"
	diff := "<<<<<<< SEARCH\nline2\n=======\n>>>>>>> REPLACE"
	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(result, "line2") {
		t.Fatal("line2 should be deleted")
	}
	if !strings.Contains(result, "line1") || !strings.Contains(result, "line3") {
		t.Fatal("line1 and line3 should remain")
	}
}

func TestApplyDiff_EmptySearchAppends(t *testing.T) {
	diff := "<<<<<<< SEARCH\n=======\nnew content\n>>>>>>> REPLACE"
	result, err := ApplyDiff("existing content", diff)
	if err != nil {
		t.Fatal(err)
	}
	if result != "existing content\nnew content" {
		t.Fatalf("got %q", result)
	}
}

func TestApplyDiff_PlaceholderComment(t *testing.T) {
	original := "/* some existing CSS */\nbody { margin: 0; }"
	diff := "<<<<<<< SEARCH\n/* Add any existing CSS content here */\n=======\n.assignee {\n    font-style: italic;\n}\n>>>>>>> REPLACE"

	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "body { margin: 0; }") {
		t.Fatal("Original should remain")
	}
	if !strings.Contains(result, ".assignee {") {
		t.Fatal("New content should be appended")
	}
}

func TestApplyDiff_PlaceholderOnEmpty(t *testing.T) {
	diff := "<<<<<<< SEARCH\n/* Add your code here */\n=======\n.new-class { color: red; }\n>>>>>>> REPLACE"
	result, err := ApplyDiff("", diff)
	if err != nil {
		t.Fatal(err)
	}
	if result != ".new-class { color: red; }" {
		t.Fatalf("got %q", result)
	}
}

func TestApplyDiff_CRLFLineEndings(t *testing.T) {
	original := "    private String description;\r\n\r\n    @Column(nullable = false)\r\n    private boolean completed;"
	diff := "<<<<<<< SEARCH\n    private String description;\n\n    @Column(nullable = false)\n    private boolean completed;\n=======\n    private String description;\n\n    private String assignee;\n\n    @Column(nullable = false)\n    private boolean completed;\n>>>>>>> REPLACE"

	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "private String assignee;") {
		t.Fatal("Should contain assignee field")
	}
}

func TestApplyDiff_IndentationDifferences(t *testing.T) {
	original := "private String description;\n@Column(nullable = false)\nprivate boolean completed;"
	diff := "<<<<<<< SEARCH\n    private String description;\n    @Column(nullable = false)\n    private boolean completed;\n=======\n    private String description;\n    private String assignee;\n    @Column(nullable = false)\n    private boolean completed;\n>>>>>>> REPLACE"

	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "private String assignee;") {
		t.Fatal("Fuzzy matching should handle indentation differences")
	}
}

func TestApplyDiff_CollapsedEmptyLines(t *testing.T) {
	original := "    private String description;\n\n\n    @Column\n    private boolean completed;"
	diff := "<<<<<<< SEARCH\n    private String description;\n\n    @Column\n    private boolean completed;\n=======\n    private String description;\n    private String assignee;\n    @Column\n    private boolean completed;\n>>>>>>> REPLACE"

	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "private String assignee;") {
		t.Fatal("Collapsed empty line matching should work")
	}
}

func TestApplyDiff_AppendPattern(t *testing.T) {
	original := ".task-list li.done .task-main {\n    text-decoration: line-through;\n    opacity: 0.6;\n}"
	diff := "<<<<<<< SEARCH\n.task-list li.done .task-main {\n    text-decoration: line-through;\n    opacity: 0.6;\n}\n=======\n.task-list li.done .task-main {\n    text-decoration: line-through;\n    opacity: 0.6;\n}\n\n.assignee {\n    font-style: italic;\n}\n>>>>>>> REPLACE"

	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, ".assignee {") {
		t.Fatal("Append pattern should add new content")
	}
	if !strings.Contains(result, "text-decoration: line-through") {
		t.Fatal("Original content should remain")
	}
}

func TestApplyDiff_HTMLPlaceholder(t *testing.T) {
	original := "<html><body></body></html>"
	diff := "<<<<<<< SEARCH\n<!-- Add existing content here -->\n=======\n<div>New content</div>\n>>>>>>> REPLACE"

	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "<html>") || !strings.Contains(result, "<div>New content</div>") {
		t.Fatal("HTML placeholder should append content")
	}
}

func TestApplyDiff_LineCommentPlaceholder(t *testing.T) {
	original := "var x = 1;"
	diff := "<<<<<<< SEARCH\n// Add your existing code here\n=======\nvar y = 2;\n>>>>>>> REPLACE"

	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "var x = 1;") || !strings.Contains(result, "var y = 2;") {
		t.Fatal("Line comment placeholder should append")
	}
}

func TestApplyDiff_HashPlaceholder(t *testing.T) {
	original := "import os"
	diff := "<<<<<<< SEARCH\n# Add your existing code here\n=======\nimport sys\n>>>>>>> REPLACE"

	result, err := ApplyDiff(original, diff)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result, "import os") || !strings.Contains(result, "import sys") {
		t.Fatal("Hash comment placeholder should append")
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
		{"andExpected", "andExpect", 2},
	}
	for _, tt := range tests {
		got := levenshtein(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestParseDiffBlocks(t *testing.T) {
	diff := "<<<<<<< SEARCH\nold1\n=======\nnew1\n>>>>>>> REPLACE\n\n<<<<<<< SEARCH\nold2\n=======\nnew2\n>>>>>>> REPLACE"
	blocks := parseDiffBlocks(diff)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2", len(blocks))
	}
	if blocks[0].search != "old1" || blocks[0].replace != "new1" {
		t.Fatalf("block 0: got %q/%q", blocks[0].search, blocks[0].replace)
	}
	if blocks[1].search != "old2" || blocks[1].replace != "new2" {
		t.Fatalf("block 1: got %q/%q", blocks[1].search, blocks[1].replace)
	}
}
