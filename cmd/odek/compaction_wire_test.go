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
		found := false
		for _, line := range strings.Split(string(b), "\n") {
			s := strings.TrimSpace(line)
			if strings.HasPrefix(s, "Compaction:") && strings.Contains(s, "resolved.Compaction") {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s must copy resolved.Compaction onto odek.Config", name)
		}
	}
}
