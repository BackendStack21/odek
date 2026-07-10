package memory

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/BackendStack21/odek/internal/memory/extended"
)

type dummyLLM struct{}

func (d *dummyLLM) SimpleCall(_ context.Context, _, _ string) (string, error) {
	return "", nil
}

func extendedEnabledCfg() MemoryConfig {
	cfg := DefaultMemoryConfig()
	enabled := true
	cfg.Extended = &extended.Config{Enabled: &enabled}
	return cfg
}

func TestMemoryToolAddAtom(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, extendedEnabledCfg())
	mm.InitExtended(&dummyLLM{}, "")
	tool := NewMemoryTool(mm)
	res, _ := tool.Call(`{"action":"add_atom","content":"I prefer dark mode","atom_type":"preference"}`)
	var out map[string]any
	if err := json.Unmarshal([]byte(res), &out); err != nil {
		t.Fatalf("invalid JSON response: %v", err)
	}
	if out["success"] != true {
		t.Errorf("expected success, got %v", out)
	}
}

func TestMemoryToolAddAtomInvalidType(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, extendedEnabledCfg())
	mm.InitExtended(&dummyLLM{}, "")
	tool := NewMemoryTool(mm)
	res, _ := tool.Call(`{"action":"add_atom","content":"x","atom_type":"not_a_type"}`)
	var out map[string]any
	_ = json.Unmarshal([]byte(res), &out)
	if out["success"] != false {
		t.Errorf("expected failure for invalid atom_type, got %v", out)
	}
}

func TestMemoryToolAddAtomDisabled(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)
	res, _ := tool.Call(`{"action":"add_atom","content":"x"}`)
	var out map[string]any
	_ = json.Unmarshal([]byte(res), &out)
	if out["success"] != false {
		t.Errorf("expected failure when extended memory disabled, got %v", out)
	}
}

func TestMemoryToolPinAtom(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, extendedEnabledCfg())
	mm.InitExtended(&dummyLLM{}, "")
	tool := NewMemoryTool(mm)
	res, _ := tool.Call(`{"action":"add_atom","content":"pin me"}`)
	var addOut map[string]any
	_ = json.Unmarshal([]byte(res), &addOut)

	atoms, _ := mm.Extended().List()
	if len(atoms) != 1 {
		t.Fatal("expected 1 atom")
	}
	pinRes, _ := tool.Call(`{"action":"pin_atom","atom_id":"` + atoms[0].ID + `"}`)
	var pinOut map[string]any
	_ = json.Unmarshal([]byte(pinRes), &pinOut)
	if pinOut["success"] != true {
		t.Errorf("expected pin success, got %v", pinOut)
	}
}

func TestMemoryToolListQuarantine(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, extendedEnabledCfg())
	mm.InitExtended(&dummyLLM{}, "")
	_ = mm.Extended().AddAtom(nil, extended.MemoryAtom{Text: "external", SourceClass: extended.SourceWeb})

	tool := NewMemoryTool(mm)
	res, _ := tool.Call(`{"action":"list_quarantine"}`)
	var out map[string]any
	_ = json.Unmarshal([]byte(res), &out)
	if out["success"] != true {
		t.Errorf("expected list_quarantine success, got %v", out)
	}
}

func TestMemoryToolForgetAtom(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, extendedEnabledCfg())
	mm.InitExtended(&dummyLLM{}, "")
	_ = mm.Extended().AddAtom(nil, extended.MemoryAtom{Text: "forget me", SourceClass: extended.SourceUserSaid})
	atoms, _ := mm.Extended().List()
	if len(atoms) != 1 {
		t.Fatal("expected 1 atom")
	}

	tool := NewMemoryTool(mm)
	res, _ := tool.Call(`{"action":"forget_atom","atom_id":"` + atoms[0].ID + `"}`)
	var out map[string]any
	_ = json.Unmarshal([]byte(res), &out)
	if out["success"] != true {
		t.Errorf("expected forget success, got %v", out)
	}
}

func TestMemoryToolUnknownAction(t *testing.T) {
	mm := NewMemoryManager(t.TempDir(), &dummyLLM{}, DefaultMemoryConfig())
	tool := NewMemoryTool(mm)
	res, _ := tool.Call(`{"action":"no_such_action"}`)
	var out map[string]any
	_ = json.Unmarshal([]byte(res), &out)
	if out["success"] != false {
		t.Errorf("expected failure for unknown action, got %v", out)
	}
}
