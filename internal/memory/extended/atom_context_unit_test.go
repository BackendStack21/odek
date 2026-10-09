package extended

import (
	"context"
	"testing"
)

// Atoms stored without a caller context fall back to the manager's current
// session and project; a caller-supplied context is kept untouched.
func TestAddAtomsContextFallbackAndOverride(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = boolPtr(true)
	em := New(t.TempDir(), nil, cfg)
	em.SetSessionContext("sess-default", "/proj-default")

	err := em.addAtoms(context.Background(), []MemoryAtom{
		{Text: "User prefers tabs over spaces", Type: TypePreference},
		{Text: "User deploys with make release", Type: TypePreference,
			Context: AtomContext{SessionID: "sess-explicit", Project: "/proj-explicit"}},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	atoms, _ := em.List()
	if len(atoms) != 2 {
		t.Fatalf("want 2 atoms, got %d", len(atoms))
	}
	for _, a := range atoms {
		switch a.Text {
		case "User prefers tabs over spaces":
			if a.Context.SessionID != "sess-default" || a.Context.Project != "/proj-default" {
				t.Errorf("fallback context = %+v", a.Context)
			}
		default:
			if a.Context.SessionID != "sess-explicit" || a.Context.Project != "/proj-explicit" {
				t.Errorf("explicit context overwritten: %+v", a.Context)
			}
		}
	}
}
