package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFormatResponseLongCodePreservesBody(t *testing.T) {
	for _, body := range []string{strings.Repeat("x", 4090), strings.Repeat("x", 6000), strings.Repeat("世界", 2000), strings.Repeat("line\n", 2000)} {
		chunks, err := FormatResponse("intro\n```go\n" + body + "\n```\nafter")
		if err != nil {
			t.Fatal(err)
		}
		var rebuilt strings.Builder
		for _, c := range chunks {
			if len(c) > 4096 || !utf8.ValidString(c) {
				t.Fatalf("invalid chunk size or UTF8: %d bytes", len(c))
			}
			if !strings.Contains(c, "```") {
				continue
			}
			if !strings.HasPrefix(c, "```go\n") {
				t.Fatalf("missing language marker: %.40q", c)
			}
			c = strings.TrimSuffix(c, "\n")
			if !strings.HasSuffix(c, "\n```") {
				t.Fatalf("missing closing fence: %.40q", c)
			}
			rebuilt.WriteString(strings.TrimSuffix(strings.TrimPrefix(c, "```go\n"), "\n```"))
		}
		if rebuilt.String() != body {
			t.Fatalf("code changed: got %d bytes want %d", rebuilt.Len(), len(body))
		}
	}
}
