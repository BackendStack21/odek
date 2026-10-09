package extended

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func perfAtomID(i int) string { return fmt.Sprintf("a1b2c3d4e5f6a7b8c9d0e1f2a3b4%04x", i) }

func TestRED_Extended_ListParsesAtomsJSONOncePerChange(t *testing.T) {
	s := NewAtomStore(t.TempDir())
	for i := 0; i < 5; i++ {
		a := MemoryAtom{ID: perfAtomID(i), Text: "atom text", CreatedAt: time.Now().Add(time.Duration(i) * time.Second)}
		if err := s.Add(a, 1000); err != nil {
			t.Fatal(err)
		}
	}
	before := s.atomsParses.Load()
	for i := 0; i < 5; i++ {
		got, err := s.List()
		if err != nil || len(got) != 5 {
			t.Fatalf("List = %d, %v", len(got), err)
		}
	}
	if n := s.atomsParses.Load() - before; n > 1 {
		t.Fatalf("5 Lists parsed atoms.json %d times, want 1", n)
	}

	// Own writes are visible immediately.
	if err := s.Pin(perfAtomID(2), true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.List()
	pinned := 0
	for _, a := range got {
		if a.Pin {
			pinned++
		}
	}
	if pinned != 1 {
		t.Fatalf("pin not visible after write: %d pinned", pinned)
	}

	// An external rewrite (another process) is picked up too.
	data, err := os.ReadFile(s.atomsFile)
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(data), `"pin": true`, `"pin": false`, 1)
	if edited == string(data) {
		t.Fatal("fixture: pin marker not found")
	}
	if err := os.WriteFile(s.atomsFile, []byte(edited), 0600); err != nil {
		t.Fatal(err)
	}
	got, _ = s.List()
	for _, a := range got {
		if a.Pin {
			t.Fatal("external atoms.json edit not picked up")
		}
	}
}
