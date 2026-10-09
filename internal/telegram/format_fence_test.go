package telegram

import (
	"strings"
	"testing"
)

func fenceOut(t *testing.T, in string) []string {
	t.Helper()
	chunks, err := FormatResponse(in)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.Join(chunks, "\n"), "\n")
}

// Text after a fence marker must not stay live MarkdownV2: a fence line is the
// marker plus at most a plain language tag.
func TestRED_FenceLineTrailingTextNeutralised(t *testing.T) {
	out := fenceOut(t, "```*bold* [x](http://e)\ncode\n``` done. ok!")
	for _, l := range out {
		if strings.HasPrefix(l, "```") && l != "```" && l != "```*bold*" {
			if strings.ContainsAny(l[3:], " *[]()!.") {
				t.Fatalf("fence line carries live markup: %q", l)
			}
		}
	}
	if out[0] != "```" {
		t.Fatalf("opening fence = %q, want bare fence", out[0])
	}
	joined := strings.Join(out, "\n")
	if strings.Contains(joined, "``` done") || strings.Contains(joined, " done. ok!") {
		t.Fatalf("closing fence kept raw trailing text: %q", joined)
	}
	if !strings.Contains(joined, `done\. ok\!`) {
		t.Fatalf("trailing text after the closing fence is not escaped: %q", joined)
	}
}

func TestFenceLanguageTagKept(t *testing.T) {
	out := fenceOut(t, "```go\nx := 1\n```")
	if out[0] != "```go" || out[len(out)-1] != "```" {
		t.Fatalf("language tag lost: %q", out)
	}
}
