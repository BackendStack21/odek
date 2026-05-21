package telegram

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// FindCommand tests
// ---------------------------------------------------------------------------

func TestFindCommand_Exists(t *testing.T) {
	knownCommands := []string{"start", "help", "new", "stats", "stop", "mode"}

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
	unknownNames := []string{"unknown", "foo", "delete", "", "restart"}

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

// ---------------------------------------------------------------------------
// All handler outputs are non-empty and error-free
// ---------------------------------------------------------------------------

func TestAllHandlers_ReturnNoError(t *testing.T) {
	for _, cmd := range DefaultCommands {
		t.Run(cmd.Command, func(t *testing.T) {
			got, err := cmd.Handler("")
			if err != nil {
				t.Errorf("%s handler returned error: %v", cmd.Command, err)
			}
			if got == "" {
				t.Errorf("%s handler returned empty string", cmd.Command)
			}
		})
	}
}
