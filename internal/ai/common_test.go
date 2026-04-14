package ai

import (
	"strings"
	"testing"
)

func TestSplitDiffIntoChunks_Empty(t *testing.T) {
	result := splitDiffIntoChunks("", 1000, 5)
	if len(result.chunks) != 1 || result.chunks[0] != "" {
		t.Fatal("Empty diff should produce one empty chunk")
	}
	if result.wasTruncated {
		t.Fatal("Should not be truncated")
	}
}

func TestSplitDiffIntoChunks_SmallDiff(t *testing.T) {
	result := splitDiffIntoChunks("small diff", 1000, 5)
	if len(result.chunks) != 1 || result.chunks[0] != "small diff" {
		t.Fatal("Small diff should be one chunk")
	}
}

func TestSplitDiffIntoChunks_LargeDiff(t *testing.T) {
	// Create a diff larger than maxCharsPerChunk
	diff := strings.Repeat("line of diff content\n", 100)
	result := splitDiffIntoChunks(diff, 500, 10)
	if len(result.chunks) < 2 {
		t.Fatalf("Expected multiple chunks, got %d", len(result.chunks))
	}
	// All content should be covered
	total := 0
	for _, c := range result.chunks {
		total += len(c)
	}
	if total != len(diff) {
		t.Fatalf("Total chunk size %d != diff size %d", total, len(diff))
	}
}

func TestSplitDiffIntoChunks_Truncated(t *testing.T) {
	diff := strings.Repeat("x", 3000)
	result := splitDiffIntoChunks(diff, 1000, 2) // max 2 chunks
	if !result.wasTruncated {
		t.Fatal("Should be truncated when exceeding maxChunks")
	}
	if len(result.chunks) != 2 {
		t.Fatalf("Expected 2 chunks, got %d", len(result.chunks))
	}
}

func TestFindSplitIndex_PrefersNewline(t *testing.T) {
	text := "hello\nworld\nfoo\nbar"
	idx := findSplitIndex(text, 12)
	if text[idx] != '\n' {
		t.Fatalf("Should split at newline, got index %d", idx)
	}
}

func TestFindSplitIndex_NoNewline(t *testing.T) {
	text := "helloworldfoobar"
	idx := findSplitIndex(text, 8)
	if idx != 8 {
		t.Fatalf("No newline: should split at maxChars, got %d", idx)
	}
}

func TestBuildUserMessage(t *testing.T) {
	msg := buildUserMessage("Fix bug", "Description here", "diff content", 1, 1, false)
	if !strings.Contains(msg, "Fix bug") {
		t.Fatal("Should contain PR title")
	}
	if !strings.Contains(msg, "Description here") {
		t.Fatal("Should contain PR body")
	}
	if !strings.Contains(msg, "diff content") {
		t.Fatal("Should contain diff")
	}
	if strings.Contains(msg, "chunk") {
		t.Fatal("Single chunk should not mention chunk number")
	}
}

func TestBuildUserMessage_MultiChunk(t *testing.T) {
	msg := buildUserMessage("Fix", "", "diff", 2, 3, false)
	if !strings.Contains(msg, "2/3") {
		t.Fatal("Should contain chunk number")
	}
}

func TestBuildUserMessage_Retry(t *testing.T) {
	msg := buildUserMessage("Fix", "", "diff", 1, 1, true)
	if !strings.Contains(msg, "truncated") {
		t.Fatal("Retry message should mention truncation")
	}
}

func TestTruncateDiff(t *testing.T) {
	short := "short"
	if truncateDiff(short, 100) != short {
		t.Fatal("Short diff should not be truncated")
	}

	long := strings.Repeat("x", 200)
	result := truncateDiff(long, 100)
	if !strings.Contains(result, "truncated") {
		t.Fatal("Long diff should be truncated with notice")
	}
	if len(result) < 100 {
		t.Fatal("Truncated diff should be at least maxChars")
	}
}

func TestResolveModel(t *testing.T) {
	if resolveModel("override", "default") != "override" {
		t.Fatal("Should use override when provided")
	}
	if resolveModel("", "default") != "default" {
		t.Fatal("Should use default when override is empty")
	}
}

func TestResolvePrompt(t *testing.T) {
	if resolvePrompt("custom") != "custom" {
		t.Fatal("Should use custom prompt")
	}
	if resolvePrompt("") != DefaultSystemPrompt {
		t.Fatal("Should use default when empty")
	}
}

func TestIsPromptTooLong(t *testing.T) {
	tests := []struct {
		body string
		want bool
	}{
		{"maximum context length exceeded", true},
		{"too many tokens in prompt", true},
		{"max_completion_tokens exceeded", true},
		{"prompt is too long", true},
		{"context length exceeded", true},
		{"some other error", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isPromptTooLong(tt.body)
		if got != tt.want {
			t.Errorf("isPromptTooLong(%q) = %v, want %v", tt.body, got, tt.want)
		}
	}
}
