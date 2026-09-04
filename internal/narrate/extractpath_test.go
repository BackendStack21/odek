package narrate

import "testing"

// extractPath scanned for the first quote pair instead of decoding JSON —
// an escaped quote inside the path truncated it (extractShell was fixed
// for exactly this failure; extractPath wasn't).
func TestExtractPath_EscapedQuoteInPath(t *testing.T) {
	got := extractPath(`{"path":"we\"ird.go","line":1}`)
	if got != `we"ird.go` {
		t.Fatalf("extractPath = %q, want %q (escaped quotes must decode)", got, `we"ird.go`)
	}
}

func TestExtractPath_PlainPathStillWorks(t *testing.T) {
	if got := extractPath(`{"path":"/a/b/main.go"}`); got != "main.go" {
		t.Fatalf("plain path: %q, want main.go", got)
	}
	if got := extractPath(`{"file":"/tmp/x.txt"}`); got != "x.txt" {
		t.Fatalf("file key: %q, want x.txt", got)
	}
}
