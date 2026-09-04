package events

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// SECURITY.md (runtime events): the JSONL sink is created with 0600
// permissions and REFUSES to follow a symlink planted at the target path —
// an attacker-planted symlink could otherwise redirect the event stream
// (session IDs, token counts) over an arbitrary file. Unpinned until now.
func TestJSONLSink_RefusesSymlinkAndMode0600(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.log")
	if err := os.WriteFile(target, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "events.jsonl")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenJSONLSink(link); err == nil {
		t.Fatal("OpenJSONLSink must refuse a symlink at the target path")
	}

	// Fresh path: 0600 perms and events land on disk per Write.
	fresh := filepath.Join(dir, "fresh.jsonl")
	sink, err := OpenJSONLSink(fresh)
	if err != nil {
		t.Fatalf("OpenJSONLSink: %v", err)
	}
	if err := sink.Write(Event{Type: TypeRunStarted, Timestamp: time.Now().UTC()}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := sink.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	fi, err := os.Stat(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0600 {
		t.Errorf("sink mode = %o, want 0600", fi.Mode().Perm())
	}
	data, err := os.ReadFile(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), string(TypeRunStarted)) {
		t.Errorf("event not persisted: %q", data)
	}
}
