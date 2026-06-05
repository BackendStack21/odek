package telegram

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// FindCommand tests
// ---------------------------------------------------------------------------

func TestFindCommand_Exists(t *testing.T) {
	knownCommands := []string{"start", "help", "new", "stats", "stop", "mode", "restart"}

	for _, name := range knownCommands {
		t.Run(name, func(t *testing.T) {
			cmd := FindCommand(name)
			if cmd == nil {
				t.Fatalf("FindCommand(%q) returned nil, expected a descriptor", name)
			}
			if cmd.Command != name {
				t.Errorf("FindCommand(%q).Command = %q, want %q", name, cmd.Command, name)
			}
			if cmd.Handler == nil {
				t.Errorf("FindCommand(%q).Handler is nil, expected a function", name)
			}
		})
	}
}

func TestFindCommand_Unknown(t *testing.T) {
	unknownNames := []string{"unknown", "foo", "delete", ""}

	for _, name := range unknownNames {
		t.Run(name, func(t *testing.T) {
			cmd := FindCommand(name)
			if cmd != nil {
				t.Errorf("FindCommand(%q) = %+v, want nil", name, cmd)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// CommandDescriptors tests
// ---------------------------------------------------------------------------

func TestCommandDescriptors_Count(t *testing.T) {
	descs := CommandDescriptors()
	if got, want := len(descs), len(DefaultCommands); got != want {
		t.Errorf("CommandDescriptors() returned %d items, want %d", got, want)
	}
}

func TestCommandDescriptors_Values(t *testing.T) {
	descs := CommandDescriptors()

	// Build a lookup from the result.
	got := make(map[string]string)
	for _, d := range descs {
		got[d.Command] = d.Description
	}

	// Verify against DefaultCommands.
	for _, expected := range DefaultCommands {
		desc, ok := got[expected.Command]
		if !ok {
			t.Errorf("CommandDescriptors() missing command %q", expected.Command)
			continue
		}
		if desc != expected.Description {
			t.Errorf("CommandDescriptors()[%q].Description = %q, want %q",
				expected.Command, desc, expected.Description)
		}
	}
}

// ---------------------------------------------------------------------------
// Handler output content tests
// ---------------------------------------------------------------------------

func TestStartHandler_ContainsOdek(t *testing.T) {
	cmd := FindCommand("start")
	if cmd == nil {
		t.Fatal("start command not found")
	}
	got, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("start handler returned error: %v", err)
	}
	if !strings.Contains(got, "odek") {
		t.Errorf("start handler output does not contain 'odek':\n%s", got)
	}
}

func TestHelpHandler_ContainsStart(t *testing.T) {
	cmd := FindCommand("help")
	if cmd == nil {
		t.Fatal("help command not found")
	}
	got, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("help handler returned error: %v", err)
	}
	if !strings.Contains(got, "/start") {
		t.Errorf("help handler output does not contain '/start':\n%s", got)
	}
}

func TestNewHandler_ContainsReset(t *testing.T) {
	cmd := FindCommand("new")
	if cmd == nil {
		t.Fatal("new command not found")
	}
	got, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("new handler returned error: %v", err)
	}
	if !strings.Contains(got, "reset") && !strings.Contains(got, "Reset") {
		t.Errorf("new handler output does not contain 'reset':\n%s", got)
	}
}

func TestStatsHandler_ContainsStats(t *testing.T) {
	cmd := FindCommand("stats")
	if cmd == nil {
		t.Fatal("stats command not found")
	}
	got, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("stats handler returned error: %v", err)
	}
	if !strings.Contains(got, "Stats") && !strings.Contains(got, "stats") {
		t.Errorf("stats handler output does not contain 'Stats':\n%s", got)
	}
}

func TestStopHandler_ContainsStop(t *testing.T) {
	cmd := FindCommand("stop")
	if cmd == nil {
		t.Fatal("stop command not found")
	}
	got, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("stop handler returned error: %v", err)
	}
	if !strings.Contains(got, "Stop") && !strings.Contains(got, "stop") {
		t.Errorf("stop handler output does not contain 'Stop':\n%s", got)
	}
}

func TestModeHandler_ContainsMode(t *testing.T) {
	cmd := FindCommand("mode")
	if cmd == nil {
		t.Fatal("mode command not found")
	}
	got, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("mode handler returned error: %v", err)
	}
	if !strings.Contains(got, "Mode") && !strings.Contains(got, "mode") {
		t.Errorf("mode handler output does not contain 'Mode':\n%s", got)
	}
}

func TestRestartHandler_ContainsRestart(t *testing.T) {
	cmd := FindCommand("restart")
	if cmd == nil {
		t.Fatal("restart command not found")
	}
	got, err := cmd.Handler("")
	if err != nil {
		t.Fatalf("restart handler returned error: %v", err)
	}
	if !strings.Contains(got, "Restarting") && !strings.Contains(got, "restart") {
		t.Errorf("restart handler output does not contain 'Restarting':\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// All handler outputs are non-empty and error-free
// ---------------------------------------------------------------------------

func TestAllHandlers_ReturnNoError(t *testing.T) {
	// Override HOME so plan handlers don't touch real ~/.odek/plans.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	// Commands handled entirely by telegram.go's OnCommand — their
	// handlers are stubs that return "".
	inlineOnly := map[string]bool{
		"sessions": true, "resume": true, "prune": true,
		"plan": true, "plan_resume": true,
		"schedule": true, "schedules": true,
	}

	for _, cmd := range DefaultCommands {
		t.Run(cmd.Command, func(t *testing.T) {
			got, err := cmd.Handler("")
			if err != nil {
				t.Errorf("%s handler returned error: %v", cmd.Command, err)
			}
			if got == "" && !inlineOnly[cmd.Command] {
				t.Errorf("%s handler returned empty string", cmd.Command)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Plan command handler tests
// ---------------------------------------------------------------------------

func TestPlansHandler_Empty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, err := plansHandler("")
	if err != nil {
		t.Fatalf("plansHandler: %v", err)
	}
	if !strings.Contains(got, "No plans found") {
		t.Errorf("expected 'No plans found', got: %s", got)
	}
}

func TestPlansHandler_WithPlans(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "test-plan.md"), []byte("# Test Plan\n\nDo something."), 0644)

	got, err := plansHandler("")
	if err != nil {
		t.Fatalf("plansHandler: %v", err)
	}
	if !strings.Contains(got, "test-plan") {
		t.Errorf("expected 'test-plan' in output, got: %s", got)
	}
	if !strings.Contains(got, "/plan_view") {
		t.Errorf("expected '/plan_view' hint, got: %s", got)
	}
}

func TestPlanViewHandler_NoSlug(t *testing.T) {
	got, err := planViewHandler("")
	if err != nil {
		t.Fatalf("planViewHandler: %v", err)
	}
	if !strings.Contains(got, "Usage") {
		t.Errorf("expected usage message, got: %s", got)
	}
}

func TestPlanViewHandler_Found(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "my-plan.md"), []byte("# My Plan\n\nStep 1. Step 2."), 0644)

	got, err := planViewHandler("my-plan")
	if err != nil {
		t.Fatalf("planViewHandler: %v", err)
	}
	if !strings.Contains(got, "My Plan") {
		t.Errorf("expected plan content, got: %s", got)
	}
	if !strings.Contains(got, "Step 1") {
		t.Errorf("expected 'Step 1' in content, got: %s", got)
	}
}

func TestPlanViewHandler_NotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, err := planViewHandler("nonexistent")
	if err != nil {
		t.Fatalf("planViewHandler: %v", err)
	}
	if !strings.Contains(got, "❌") {
		t.Errorf("expected error message, got: %s", got)
	}
}

func TestPlanDeleteHandler_NoSlug(t *testing.T) {
	got, err := planDeleteHandler("")
	if err != nil {
		t.Fatalf("planDeleteHandler: %v", err)
	}
	if !strings.Contains(got, "Usage") {
		t.Errorf("expected usage message, got: %s", got)
	}
}

func TestPlanDeleteHandler_Success(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".odek", "plans")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "delete-me.md"), []byte("bye"), 0644)

	got, err := planDeleteHandler("delete-me")
	if err != nil {
		t.Fatalf("planDeleteHandler: %v", err)
	}
	if !strings.Contains(got, "deleted") {
		t.Errorf("expected 'deleted' message, got: %s", got)
	}
	// Verify file is gone.
	if _, err := os.Stat(filepath.Join(dir, "delete-me.md")); !os.IsNotExist(err) {
		t.Error("plan file still exists after delete")
	}
}

func TestPlanDeleteHandler_NotFound(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	got, err := planDeleteHandler("nonexistent")
	if err != nil {
		t.Fatalf("planDeleteHandler: %v", err)
	}
	if !strings.Contains(got, "❌") {
		t.Errorf("expected error message, got: %s", got)
	}
}

func TestPlanHandler_Stub(t *testing.T) {
	// planHandler is a stub — real plan creation is handled inline
	// by telegram.go's OnCommand which spawns an agent.
	got, err := planHandler("")
	if err != nil {
		t.Fatalf("planHandler: %v", err)
	}
	if got != "" {
		t.Errorf("planHandler should return empty (handled inline), got: %s", got)
	}
}

func TestPlanResumeHandler_Stub(t *testing.T) {
	got, err := planResumeHandler("")
	if err != nil {
		t.Fatalf("planResumeHandler: %v", err)
	}
	if got != "" {
		t.Errorf("planResumeHandler should return empty (handled inline), got: %s", got)
	}
}

func TestFindCommand_PlanCommands(t *testing.T) {
	planCommands := []string{"plan", "plans", "plan_view", "plan_delete", "plan_resume"}
	for _, name := range planCommands {
		t.Run(name, func(t *testing.T) {
			if cmd := FindCommand(name); cmd == nil {
				t.Errorf("FindCommand(%q) returned nil", name)
			}
		})
	}
}
