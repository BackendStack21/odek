package telegram

import (
	"strings"
	"testing"
)

// TestSplitChunks_CodeBlockIntact verifies that a code block smaller than
// maxBytes stays in one chunk — \n\n inside the block doesn't trigger a split.
func TestSplitChunks_CodeBlockIntact(t *testing.T) {
	// Code block with internal \n\n — should NOT be split
	before := strings.Repeat("x", 3500)
	code := "\n\n```\nsome code\n\ninside block\n```"
	after := "\n\nfinal text"

	input := before + code + after

	got := splitChunks(input, 4096)

	// The code block must appear BALANCED (even number of ```) in one chunk.
	// Since the code block fits within 4096, it should be entirely in one chunk.
	foundComplete := false
	for _, chunk := range got {
		opens := strings.Count(chunk, "```")
		if opens == 2 {
			// Both open and close in same chunk
			foundComplete = true
			if !strings.Contains(chunk, "some code") {
				t.Error("code block appears intact but content missing")
			}
		}
		if opens == 1 {
			t.Errorf("unbalanced ``` in chunk (len=%d): %q", len(chunk), chunk[:min(80, len(chunk))])
		}
	}

	if !foundComplete {
		t.Error("no chunk contains the complete code block")
	}

	// All content must be present
	combined := strings.Join(got, "")
	if !strings.Contains(combined, "some code") {
		t.Error("lost code block content")
	}
	if !strings.Contains(combined, "final text") {
		t.Error("lost after-text content")
	}
}

func TestSplitChunks_LongCodeBlockKeepsEachChunkParseable(t *testing.T) {
	input := "```\n" + strings.Repeat("x ", 3000) + "\n```"
	chunks := splitChunks(input, 4096)
	if len(chunks) < 2 {
		t.Fatalf("expected long code block to split, got %d chunk(s)", len(chunks))
	}
	for i, chunk := range chunks {
		if strings.Count(chunk, "```")%2 != 0 {
			t.Fatalf("chunk %d has unbalanced code fences", i)
		}
		if len(chunk) > 4096 {
			t.Fatalf("chunk %d exceeds Telegram limit: %d", i, len(chunk))
		}
	}
}

func TestSplitChunks_MixedProseAndAdjacentCodeBlocks(t *testing.T) {
	input := "intro\n```go\n" + strings.Repeat("é\n", 3000) + "```\nafter\n```\nsecond\n```"
	chunks := splitChunks(input, 4096)
	if len(chunks) < 2 {
		t.Fatalf("expected mixed response to split, got %d chunk(s)", len(chunks))
	}
	for i, chunk := range chunks {
		if len(chunk) > 4096 || strings.Count(chunk, "```")%2 != 0 {
			t.Fatalf("chunk %d invalid: bytes=%d fences=%d", i, len(chunk), strings.Count(chunk, "```"))
		}
	}
	joined := strings.Join(chunks, "")
	for _, marker := range []string{"intro", "é", "after", "second"} {
		if !strings.Contains(joined, marker) {
			t.Fatalf("split lost content marker %q", marker)
		}
	}
}

func TestSplitChunks_MixedOversizedCodeLineKeepsLimitAndLanguage(t *testing.T) {
	for _, n := range []int{4090, 6000} {
		input := "intro\n```go\n" + strings.Repeat("x", n) + "\n```\nafter"
		chunks := splitChunks(input, 4096)
		for i, chunk := range chunks {
			if len(chunk) > 4096 || strings.Count(chunk, "```")%2 != 0 {
				t.Fatalf("n=%d chunk %d invalid: bytes=%d fences=%d", n, i, len(chunk), strings.Count(chunk, "```"))
			}
		}
		joined := strings.Join(chunks, "")
		body := strings.ReplaceAll(strings.ReplaceAll(joined, "```go", ""), "```", "")
		for _, marker := range []string{"intro", "after"} {
			if !strings.Contains(joined, marker) {
				t.Fatalf("n=%d split lost marker/content %q", n, marker)
			}
		}
		if strings.Count(body, "x") != n {
			t.Fatalf("n=%d code body was not reconstructable: got %d x bytes", n, strings.Count(body, "x"))
		}
	}
}

// TestSplitChunks_MultipleCodeBlocks verifies that multiple code blocks
// are each kept intact during chunking.
func TestSplitChunks_MultipleCodeBlocks(t *testing.T) {
	before := strings.Repeat("a", 3000)
	code1 := "\n\n```go\nfunc main() {}\n```"
	mid := "\n\n" + strings.Repeat("b", 1000)
	code2 := "\n\n```json\n{\"key\": \"val\"}\n```"

	input := before + code1 + mid + code2

	got := splitChunks(input, 4096)

	for _, chunk := range got {
		opens := strings.Count(chunk, "```")
		if opens%2 != 0 {
			t.Errorf("unbalanced ``` in chunk: %q", chunk[:min(200, len(chunk))])
		}
	}

	combined := strings.Join(got, "")
	if !strings.Contains(combined, "func main()") {
		t.Error("lost code1 content")
	}
	if !strings.Contains(combined, `"key": "val"`) {
		t.Error("lost code2 content")
	}
}

// TestSplitChunks_InlineCodeUnaffected verifies that inline `code` spans
// (single backtick) do NOT affect chunk splitting — only fenced blocks.
func TestSplitChunks_InlineCodeUnaffected(t *testing.T) {
	before := strings.Repeat("a", 3000)
	mid := "\n\nUse `code` here."
	after := "\n\n" + strings.Repeat("b", 2000)

	input := before + mid + after

	got := splitChunks(input, 4096)

	if len(got) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(got))
	}

	combined := strings.Join(got, "")
	if !strings.Contains(combined, "`code`") {
		t.Error("lost inline code content")
	}
}
