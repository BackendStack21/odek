package extended

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAssociationsLinkAndRelated(t *testing.T) {
	a := NewAssociations()
	a.Link("a", "b")
	a.Link("a", "c")
	related := a.Related("a")
	if len(related) != 2 {
		t.Fatalf("expected 2 related, got %d", len(related))
	}
	if related[0] != "b" || related[1] != "c" {
		t.Errorf("unexpected related: %v", related)
	}
	if len(a.Related("b")) != 1 || a.Related("b")[0] != "a" {
		t.Errorf("expected b related to a, got %v", a.Related("b"))
	}
}

func TestAssociationsIgnoresSelfLink(t *testing.T) {
	a := NewAssociations()
	a.Link("a", "a")
	if a.Related("a") != nil {
		t.Errorf("expected no self-link, got %v", a.Related("a"))
	}
}

func TestAssociationsRemoveAtom(t *testing.T) {
	a := NewAssociations()
	a.Link("a", "b")
	a.Link("b", "c")
	a.RemoveAtom("b")
	if a.Related("a") != nil {
		t.Errorf("expected a no longer related after remove, got %v", a.Related("a"))
	}
	if a.Related("c") != nil {
		t.Errorf("expected c no longer related after remove, got %v", a.Related("c"))
	}
}

func TestAssociationsPersistAndLoad(t *testing.T) {
	dir := t.TempDir()
	a := NewAssociationsWithDir(dir)
	a.Link("a", "b")
	if err := a.Persist(); err != nil {
		t.Fatalf("Persist failed: %v", err)
	}

	b := NewAssociationsWithDir(dir)
	related := b.Related("a")
	if len(related) != 1 || related[0] != "b" {
		t.Errorf("expected [b] after load, got %v", related)
	}

	path := filepath.Join(dir, "associations.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("associations.json mode = %04o, want 0600", info.Mode().Perm())
	}
}

func TestAssociationsLoadIgnoresInvalidID(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "associations.json"), []byte(`{"../etc/passwd":["a"],"a":["../etc/passwd"]}`), 0600)
	a := NewAssociationsWithDir(dir)
	if a.Related("../etc/passwd") != nil {
		t.Error("expected invalid IDs ignored")
	}
	if len(a.Related("a")) != 0 {
		t.Errorf("expected a has no related after invalid filter, got %v", a.Related("a"))
	}
}

func TestAssociationsLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "associations.json"), []byte("not json"), 0600)
	a := NewAssociationsWithDir(dir)
	if a.Related("a") != nil {
		t.Error("expected empty associations on invalid JSON")
	}
}

func TestAssociationsNilSafe(t *testing.T) {
	var a *Associations
	a.Link("a", "b")
	if a.Related("a") != nil {
		t.Error("expected nil related on nil assoc")
	}
	a.RemoveAtom("a")
	if err := a.Persist(); err != nil {
		t.Errorf("expected nil persist on nil assoc, got %v", err)
	}
}

func TestAssociationsLinkEmpty(t *testing.T) {
	a := NewAssociations()
	a.Link("", "b")
	a.Link("a", "")
	if a.Related("a") != nil {
		t.Error("expected empty IDs ignored")
	}
}

func TestAssociationsRelatedSorted(t *testing.T) {
	a := NewAssociations()
	a.Link("x", "c")
	a.Link("x", "a")
	a.Link("x", "b")
	related := a.Related("x")
	if related[0] != "a" || related[1] != "b" || related[2] != "c" {
		t.Errorf("expected sorted [a b c], got %v", related)
	}
}

func TestAssociationsDuplicateLinks(t *testing.T) {
	a := NewAssociations()
	a.Link("a", "b")
	a.Link("a", "b")
	related := a.Related("a")
	if len(related) != 1 {
		t.Errorf("expected 1 related after duplicate link, got %d", len(related))
	}
}
