package main

// Token-efficiency pins for the built-in tool surface. Every description and
// schema below is sent to the LLM on every turn, so boilerplate stated N times
// costs N × turns. These tests pin the deduplicated state: shared guidance is
// stated once, essays are compressed, and the heavyweight delegate_tasks
// surface stays under a fixed character budget.

import (
	"encoding/json"
	"strings"
	"testing"
)

type descTarget struct {
	name string
	desc string
}

// builtinDescriptions returns the description of every built-in tool whose
// surface this budget governs.
func builtinDescriptions(t *testing.T) []descTarget {
	t.Helper()
	b64 := &base64Tool{}
	glob := &globTool{}
	tree := &treeTool{}
	dt := &delegateTasksTool{}
	bgStart := &bgStartTool{}
	bgList := &bgListTool{}
	bgStatus := &bgStatusTool{}
	bgOutput := &bgOutputTool{}
	bgStop := &bgStopTool{}
	st := &shellTool{}
	return []descTarget{
		{"base64", b64.Description()},
		{"glob", glob.Description()},
		{"tree", tree.Description()},
		{"delegate_tasks", dt.Description()},
		{"bg_start", bgStart.Description()},
		{"bg_list", bgList.Description()},
		{"bg_status", bgStatus.Description()},
		{"bg_output", bgOutput.Description()},
		{"bg_stop", bgStop.Description()},
		{"shell", st.Description()},
	}
}

// TestToolDescriptions_NoZeroForkBoilerplate: the zero-fork implementation
// note is an internal detail, not model-facing guidance. It must not be
// repeated across tool descriptions.
func TestToolDescriptions_NoZeroForkBoilerplate(t *testing.T) {
	for _, d := range builtinDescriptions(t) {
		if strings.Contains(d.desc, "Zero-fork") {
			t.Errorf("%s description repeats the zero-fork boilerplate; it must be stated nowhere in the tool surface: %q", d.name, d.desc)
		}
		if strings.Contains(d.desc, "zero-fork") {
			t.Errorf("%s description mentions the zero-fork implementation detail: %q", d.name, d.desc)
		}
	}
}

// TestToolDescriptions_SleepWaitStatedOnce: the wake-on-complete contract is
// owned by bg_start. The passive bg_* tools carry only a two-word reminder —
// the model that started a job already read the full rule in bg_start.
func TestToolDescriptions_SleepWaitStatedOnce(t *testing.T) {
	for _, d := range builtinDescriptions(t) {
		if d.name == "bg_start" {
			if !strings.Contains(d.desc, "delivered automatically") {
				t.Errorf("bg_start must own the completion-delivery guidance, got: %q", d.desc)
			}
			continue
		}
		if strings.Contains(d.desc, "Never sleep-wait for a job") || strings.Contains(d.desc, "delivered automatically") || strings.Contains(d.desc, "see bg_start") {
			t.Errorf("%s repeats bg_start's full sleep-wait contract (keep only the short reminder): %q", d.name, d.desc)
		}
		if strings.HasPrefix(d.name, "bg_") && !strings.Contains(d.desc, "Never sleep-wait.") {
			t.Errorf("%s lacks the short no-sleep reminder: %q", d.name, d.desc)
		}
	}
}

// TestToolDescriptions_ShellRiskEssaySlimmed: the nine-value risk-class
// enumeration is an approval-gate implementation detail; the shell description
// only needs to convey that risky operations prompt and unknowns are denied.
func TestToolDescriptions_ShellRiskEssaySlimmed(t *testing.T) {
	desc := (&shellTool{}).Description()
	if !strings.Contains(desc, "Risk classes") {
		t.Fatalf("shell description must still mention Risk classes, got: %q", desc)
	}
	for _, cls := range []string{"local_write", "system_write", "network_egress", "code_execution"} {
		if strings.Contains(desc, cls) {
			t.Errorf("shell description enumerates risk class %q; the taxonomy lives in the security pillar, not the tool surface", cls)
		}
	}
	if len(desc) > 600 {
		t.Errorf("shell description too long: %d chars (want ≤600)", len(desc))
	}
}

// TestDelegateTasks_SchemaBudget: delegate_tasks is the heaviest single tool
// surface (~950–1000 tokens pre-slim). Its JSON schema must stay under a fixed
// character budget, with no duplicated guidance text between the description
// and the guidance parameter.
func TestDelegateTasks_SchemaBudget(t *testing.T) {
	dt := &delegateTasksTool{}
	b, err := json.Marshal(dt.Schema())
	if err != nil {
		t.Fatal(err)
	}
	schema := string(b)
	if len(schema) > 2800 {
		t.Errorf("delegate_tasks schema too heavy: %d chars (want ≤2800)", len(schema))
	}
	// Guidance mechanics appear in the description; the guidance param must
	// not re-teach them verbatim.
	if strings.Contains(schema, "Write the full deliverable as a flat file") {
		t.Error("delegate_tasks guidance param duplicates the artifact-delivery instruction already in the description")
	}
	for _, param := range []string{"trust_level", "max_risk", "profile"} {
		var decoded struct {
			Properties struct {
				Tasks struct {
					Items struct {
						Properties map[string]struct {
							Description string `json:"description"`
						} `json:"properties"`
					} `json:"items"`
				} `json:"tasks"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatal(err)
		}
		p, ok := decoded.Properties.Tasks.Items.Properties[param]
		if !ok {
			continue
		}
		if len(p.Description) > 220 {
			t.Errorf("delegate_tasks %s description is an essay (%d chars, want ≤220): %q", param, len(p.Description), p.Description)
		}
	}
}
