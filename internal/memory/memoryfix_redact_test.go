package memory

import (
	"context"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/memory/extended"
)

const memoryFixSecret = "ghp_abcdefghijklmnopqrstuvwxyz1234567890"

func TestRED_MemoryTiersRedactSecrets(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, extendedEnabledCfg())
	mm.InitExtended(&dummyLLM{}, "")

	mm.AppendBuffer("user", "push with my token "+memoryFixSecret)
	for _, l := range mm.GetBuffer() {
		if strings.Contains(l, memoryFixSecret) {
			t.Errorf("buffer line carries a secret: %q", l)
		}
	}

	if err := mm.episodes.Write("20261012-redact", "user shared "+memoryFixSecret, 3); err != nil {
		t.Fatal(err)
	}
	if body, err := mm.episodes.Read("20261012-redact"); err != nil || strings.Contains(body, memoryFixSecret) {
		t.Errorf("episode carries a secret (err=%v): %q", err, body)
	}

	if err := mm.AddFact("env", "deploy token is "+memoryFixSecret); err == nil {
		entries, _ := mm.facts.Entries("env")
		for _, e := range entries {
			if strings.Contains(e, memoryFixSecret) {
				t.Errorf("fact carries a secret: %q", e)
			}
		}
	}

	if err := mm.Extended().AddAtom(context.Background(), extended.MemoryAtom{Text: "my token is " + memoryFixSecret, SourceClass: extended.SourceUserSaid}); err != nil {
		t.Fatal(err)
	}
	atoms, _ := mm.Extended().List()
	for _, a := range atoms {
		if strings.Contains(a.Text, memoryFixSecret) {
			t.Errorf("atom carries a secret: %q", a.Text)
		}
	}
}
