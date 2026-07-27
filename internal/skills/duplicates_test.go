package skills

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/guard"
)

func dupTestBody(verb string) string {
	return "## Overview\n\n" +
		"Reusable procedure for running the project " + verb + " pipeline with enough body text to pass the quality gate minimum of two hundred characters comfortably.\n\n" +
		"## Step-by-Step\n\n1. make " + verb + "\n2. verify output\n\n" +
		"## Common Pitfalls\n\n- none\n\n## Verification\n\n- exit 0"
}

func autosaveTestCfg() SkillsConfig {
	cfg := DefaultSkillsConfig()
	cfg.AutoSave.MinOccurrences = 1 // bypass recurrence gate
	cfg.AutoSave.MaxPerRun = 5
	return cfg
}

// TestAutoSaveSuggestions_NearDuplicateSkipped saves a skill, then feeds a
// differently-named suggestion whose body is a light paraphrase — it must
// be reported as a duplicate instead of creating a second skill.
func TestAutoSaveSuggestions_NearDuplicateSkipped(t *testing.T) {
	dir := t.TempDir()
	existing := SkillSuggestion{Name: "build-pipeline", Heuristic: "multi-step", Body: dupTestBody("build")}
	if err := SaveSuggestion(dir, existing); err != nil {
		t.Fatal(err)
	}

	// Light paraphrase: same word set modulo a couple of tokens.
	paraphrase := strings.Replace(dupTestBody("build"), "verify output", "check output", 1)
	suggestions := []SkillSuggestion{
		{Name: "project-build-flow", Heuristic: "multi-step", Body: paraphrase},
	}
	result := AutoSaveSuggestions(suggestions, dir, "", autosaveTestCfg(), nil, guard.Config{}, false)

	if result.DuplicateOf["project-build-flow"] != "build-pipeline" {
		t.Errorf("expected duplicate mapping to build-pipeline, got %+v", result)
	}
	if len(result.Saved) != 0 {
		t.Errorf("near-duplicate must not be saved, got %v", result.Saved)
	}
}

// TestAutoSaveSuggestions_DistinctBodySaved confirms unrelated content is
// unaffected by the duplicate gate.
func TestAutoSaveSuggestions_DistinctBodySaved(t *testing.T) {
	dir := t.TempDir()
	existing := SkillSuggestion{Name: "build-pipeline", Heuristic: "multi-step", Body: dupTestBody("build")}
	if err := SaveSuggestion(dir, existing); err != nil {
		t.Fatal(err)
	}

	suggestions := []SkillSuggestion{
		{Name: "docker-debug", Heuristic: "error-recovery", Body: "## Overview\n\nDiagnose a crashed container by inspecting logs and exit codes before touching anything else.\n\n## Step-by-Step\n\n1. docker ps -a\n2. docker logs --tail 200 <id>\n3. docker inspect <id> --format '{{.State.ExitCode}}'\n\n## Common Pitfalls\n\n- Restarting before reading logs destroys the evidence\n\n## Verification\n\n- container stays up"},
	}
	result := AutoSaveSuggestions(suggestions, dir, "", autosaveTestCfg(), nil, guard.Config{}, false)
	if len(result.DuplicateOf) != 0 {
		t.Errorf("distinct body must not be flagged duplicate: %+v", result.DuplicateOf)
	}
	if len(result.Saved) != 1 {
		t.Errorf("expected save, got %+v", result)
	}
}

// TestAutoSaveSuggestions_SameNameIsUpdateNotDuplicate: re-saving an
// existing name is an update and must bypass the duplicate gate.
func TestAutoSaveSuggestions_SameNameIsUpdateNotDuplicate(t *testing.T) {
	dir := t.TempDir()
	existing := SkillSuggestion{Name: "build-pipeline", Heuristic: "multi-step", Body: dupTestBody("build")}
	if err := SaveSuggestion(dir, existing); err != nil {
		t.Fatal(err)
	}

	suggestions := []SkillSuggestion{
		{Name: "build-pipeline", Heuristic: "multi-step", Body: dupTestBody("build")},
	}
	result := AutoSaveSuggestions(suggestions, dir, "", autosaveTestCfg(), nil, guard.Config{}, false)
	if len(result.DuplicateOf) != 0 {
		t.Errorf("same-name save is an update, not a duplicate: %+v", result.DuplicateOf)
	}
	if len(result.Saved) != 1 {
		t.Errorf("expected update save, got %+v", result)
	}
}

// TestJaccard_Basics pins the similarity math edges.
func TestJaccard_Basics(t *testing.T) {
	a := map[string]bool{"x": true, "y": true}
	b := map[string]bool{"y": true, "z": true}
	if got := jaccard(a, b); got != 1.0/3.0 {
		t.Errorf("jaccard = %v, want 1/3", got)
	}
	if got := jaccard(a, a); got != 1.0 {
		t.Errorf("jaccard(a,a) = %v, want 1", got)
	}
	if got := jaccard(a, map[string]bool{}); got != 0 {
		t.Errorf("jaccard with empty set = %v, want 0", got)
	}
}
