package memory

import (
	"strings"
	"testing"
)

func TestApplyConsolidationRejectsStalePreview(t *testing.T) {
	cfg := DefaultMemoryConfig()
	mm := NewMemoryManager(t.TempDir(), nil, cfg)
	if err := mm.AddFact("user", "Prefers Go"); err != nil {
		t.Fatal(err)
	}
	before, err := mm.facts.Entries("user")
	if err != nil {
		t.Fatal(err)
	}
	preview := ConsolidationPreview{Before: before, After: []string{"Prefers Go for services"}}
	if err := mm.AddFact("user", "Uses Linux"); err != nil {
		t.Fatal(err)
	}
	if err := mm.ApplyConsolidation("user", preview); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("want stale preview rejection, got %v", err)
	}
	user, _, _ := mm.ReadFacts()
	if !strings.Contains(user, "Linux") {
		t.Fatal("concurrent fact lost")
	}
}
func TestApplyConsolidationAppliesExactSnapshot(t *testing.T) {
	cfg := DefaultMemoryConfig()
	mm := NewMemoryManager(t.TempDir(), nil, cfg)
	if err := mm.AddFact("env", "Language is Go"); err != nil {
		t.Fatal(err)
	}
	before, _ := mm.facts.Entries("env")
	if err := mm.ApplyConsolidation("env", ConsolidationPreview{Before: before, After: []string{"Go project"}}); err != nil {
		t.Fatal(err)
	}
	_, env, _ := mm.ReadFacts()
	if !strings.Contains(env, "Go project") {
		t.Fatal(env)
	}
}
