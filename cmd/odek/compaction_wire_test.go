package main

import (
	"os"
	"strings"
	"testing"
)

// TestLongLivedAgents_CopyResolvedCompaction pins that serve and Telegram
// pass resolved.Compaction into odek.Config. Those surfaces parse the flag
// / config bit; omitting the field left rolling compaction at the library
// default (off) while GET /api/config still reported it on.
func TestLongLivedAgents_CopyResolvedCompaction(t *testing.T) {
	for _, name := range []string{"serve.go", "telegram.go"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !strings.Contains(string(b), "Compaction:") || !strings.Contains(string(b), "resolved.Compaction") {
			t.Errorf("%s must copy resolved.Compaction onto odek.Config", name)
		}
	}
}
