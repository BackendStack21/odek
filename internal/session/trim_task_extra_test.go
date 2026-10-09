package session

import (
	"strings"
	"testing"
)

func TestProtectedHeadLen(t *testing.T) {
	cases := []struct {
		name string
		msgs []Message
		want int
	}{
		{"empty", nil, 0},
		{"system only", []Message{{Role: "system"}}, 1},
		{"no system", []Message{{Role: "user"}, {Role: "assistant"}}, 1},
		{"system user", []Message{{Role: "system"}, {Role: "user"}, {Role: "assistant"}}, 2},
		{"assistant only", []Message{{Role: "assistant"}}, 0},
	}
	for _, c := range cases {
		if got := protectedHeadLen(c.msgs); got != c.want {
			t.Errorf("%s: got %d want %d", c.name, got, c.want)
		}
	}
}

func TestWriteTimeTrimMarkerFollowsTaskAndOversizedTaskStillFits(t *testing.T) {
	old := MaxSessionFileBytes
	MaxSessionFileBytes = 16 * 1024
	t.Cleanup(func() { MaxSessionFileBytes = old })
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msgs := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "TASK"}}
	for i := 0; i < 20; i++ {
		msgs = append(msgs, Message{Role: "assistant", Content: strings.Repeat("y", 2048)})
	}
	sess := &Session{ID: "20260101-ffffff", Messages: msgs}
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Messages[1].Content != "TASK" || loaded.Messages[2].Role != "system" || !strings.Contains(loaded.Messages[2].Content, "Session storage limit") {
		t.Fatalf("expected task then marker, got %q / %q", loaded.Messages[1].Content, loaded.Messages[2].Content)
	}

	// A task that alone exceeds the cap must still yield a loadable file.
	huge := &Session{ID: "20260101-aaaaab", Messages: []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("z", 32*1024)},
		{Role: "assistant", Content: "ok"},
	}}
	if err := store.Save(huge); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(huge.ID); err != nil {
		t.Fatalf("oversized-task session unloadable: %v", err)
	}
}
