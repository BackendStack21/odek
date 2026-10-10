package session

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/redact"
)

func perfMessages(n int) []Message {
	var ms []Message
	for i := 0; i < n; i++ {
		ms = append(ms, Message{Role: "user", Content: strings.Repeat("u", 100)})
		ms = append(ms, Message{Role: "assistant", Content: strings.Repeat("a", 100)})
		ms = append(ms, Message{Role: "tool", Content: "ignored"})
	}
	return ms
}

func TestRED_Session_BuildConversationTextLinearAllocs(t *testing.T) {
	ms := perfMessages(300)
	allocs := testing.AllocsPerRun(5, func() { _ = BuildConversationText(ms) })
	if allocs > 5 {
		t.Fatalf("BuildConversationText allocs = %v, want <= 5", allocs)
	}
	got := BuildConversationText([]Message{{Role: "user", Content: "hi"}, {Role: "tool", Content: "x"}, {Role: "assistant", Content: "yo"}, {Role: "user"}})
	if got != "[User] hi\n[Assistant] yo\n" {
		t.Fatalf("unexpected text %q", got)
	}
}

func promptMessages(n int) []Message {
	var ms []Message
	for i := 0; i < n; i++ {
		p := "prompt " + strings.Repeat("x", i+1)
		ms = append(ms, Message{Role: "user", Content: "c", PrincipalPrompt: &p})
		ms = append(ms, Message{Role: "assistant", Content: "a"})
	}
	return ms
}

func TestRED_Session_PrincipalPromptsNotRedactedTwice(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(promptMessages(20), "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	p := "newest prompt"
	sess.Messages = append(sess.Messages, Message{Role: "user", Content: "c", PrincipalPrompt: &p})
	before := store.promptRedactions
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	if got := store.promptRedactions - before; got > 1 {
		t.Fatalf("second save redacted %d prompts, want only the new one", got)
	}
}

func TestRED_Session_PrincipalPromptMutatedInPlaceStillRedacted(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(promptMessages(5), "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	secret := "in-place-mutated-prompt-secret-value"
	redact.RegisterSecret(secret)
	// Mutate an already-persisted prompt through its pointer.
	*sess.Messages[2].PrincipalPrompt = "leak " + secret
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(store.path(sess.ID))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatal("mutated prompt below the boundary reached disk unredacted")
	}
	*sess.Messages[4].PrincipalPrompt = "leak2 " + secret
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Content: "x"})
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(store.path(sess.ID))
	if strings.Contains(string(raw), secret) {
		t.Fatal("mutated prompt leaked after append")
	}
}

func TestRED_Session_RevisionCheckAvoidsReloadOnOwnWrites(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(promptMessages(3), "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	// A stamp younger than the settle window is not trusted: a coarse file
	// clock could hide a foreign rewrite inside it. The window is widened for
	// this phase so a slow runner cannot let the stamp settle between Create
	// and the save, which would make the check depend on wall-clock speed.
	settle := revStampSettle
	revStampSettle = time.Hour
	before := store.revisionLoads
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Content: "fast"})
	err = store.SaveNoIndex(sess)
	revStampSettle = settle
	if err != nil {
		t.Fatal(err)
	}
	if got := store.revisionLoads - before; got != 1 {
		t.Fatalf("%d full loads for a write inside the settle window, want 1", got)
	}
	time.Sleep(revStampSettle + 10*time.Millisecond)
	before = store.revisionLoads
	for i := 0; i < 5; i++ {
		sess.Messages = append(sess.Messages, Message{Role: "assistant", Content: "step"})
		if err := store.SaveNoIndex(sess); err != nil {
			t.Fatal(err)
		}
		time.Sleep(revStampSettle + 10*time.Millisecond)
	}
	if got := store.revisionLoads - before; got != 0 {
		t.Fatalf("%d full loads for 5 settled self-writes, want 0", got)
	}

	// A second store (another process) advancing the session is still detected.
	other, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := other.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	theirs.Messages = append(theirs.Messages, Message{Role: "assistant", Content: "other"})
	if err := other.SaveNoIndex(theirs); err != nil {
		t.Fatal(err)
	}
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Content: "mine"})
	if err := store.SaveNoIndex(sess); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale writer: err = %v, want ErrConflict", err)
	}
}

func TestRED_Session_RevisionCheckSameSizeExternalRewriteDetected(t *testing.T) {
	dir := t.TempDir()
	store, err := NewStoreWithDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]Message{{Role: "user", Content: "aaaa"}}, "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	other, _ := NewStoreWithDir(dir)
	theirs, err := other.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	theirs.Messages[0].Content = "bbbb"
	if err := other.SaveNoIndex(theirs); err != nil {
		t.Fatal(err)
	}
	sess.Messages[0].Content = "cccc"
	if err := store.SaveNoIndex(sess); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestRED_Session_RevisionCheckRemovedFileStillDetected(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]Message{{Role: "user", Content: "a"}}, "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.path(sess.ID)); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNoIndex(sess); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want not-exist", err)
	}
}

func TestRED_Session_StepCheckpointsDoNotRewriteIndex(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]Message{{Role: "user", Content: "q"}}, "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	before := store.indexWrites
	for i := 0; i < 5; i++ {
		sess.Messages = append(sess.Messages, Message{Role: "assistant", Content: "step"})
		if err := store.SaveNoIndex(sess); err != nil {
			t.Fatal(err)
		}
	}
	if got := store.indexWrites - before; got != 0 {
		t.Fatalf("%d index rewrites for 5 step checkpoints, want 0", got)
	}
	// The end-of-turn full save always refreshes the index.
	if err := store.Save(sess); err != nil {
		t.Fatal(err)
	}
	if got := store.indexWrites - before; got != 1 {
		t.Fatalf("full save wrote the index %d times, want 1", got)
	}
	list, err := store.List(0)
	if err != nil || len(list) != 1 || !list[0].UpdatedAt.Equal(sess.UpdatedAt) {
		t.Fatalf("list after full save = %v, %v", list, err)
	}
	// A new user turn changes Turns: checkpoint must write it.
	sess.Messages = append(sess.Messages, Message{Role: "user", Content: "next"})
	before = store.indexWrites
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	if store.indexWrites-before != 1 {
		t.Fatal("turn-count change must rewrite the index")
	}
}

func TestIndexMayLag(t *testing.T) {
	base := time.Now()
	old := IndexEntry{ID: "a", Title: "t", Model: "m", Turns: 1, CreatedAt: base, UpdatedAt: base}
	next := old
	next.UpdatedAt = base.Add(time.Second)
	next.InputTokens = 9
	if !indexMayLag(old, next) {
		t.Fatal("volatile-only change within window should lag")
	}
	for name, mut := range map[string]func(*IndexEntry){
		"window":    func(e *IndexEntry) { e.UpdatedAt = base.Add(3 * time.Second) },
		"backwards": func(e *IndexEntry) { e.UpdatedAt = base.Add(-time.Second) },
		"title":     func(e *IndexEntry) { e.Title = "x" },
		"model":     func(e *IndexEntry) { e.Model = "x" },
		"pinned":    func(e *IndexEntry) { e.Pinned = true },
		"turns":     func(e *IndexEntry) { e.Turns = 2 },
		"created":   func(e *IndexEntry) { e.CreatedAt = base.Add(time.Hour) },
	} {
		n := next
		mut(&n)
		if indexMayLag(old, n) {
			t.Fatalf("%s change must not lag", name)
		}
	}
}

func TestRED_Session_ListStatsOnlyThePage(t *testing.T) {
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i := 0; i < 50; i++ {
		sess, err := store.Create([]Message{{Role: "user", Content: "q"}}, "m", "task")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, sess.ID)
	}
	before := store.listStats.Load()
	list, err := store.List(5)
	if err != nil || len(list) != 5 {
		t.Fatalf("List(5) = %d, %v", len(list), err)
	}
	if got := store.listStats.Load() - before; got > 5 {
		t.Fatalf("List(5) statted %d entries, want <= 5", got)
	}
	// A stale newest entry is skipped and replaced by the next live one.
	if err := os.Remove(store.path(list[0].ID)); err != nil {
		t.Fatal(err)
	}
	list2, err := store.List(5)
	if err != nil || len(list2) != 5 {
		t.Fatalf("List(5) after removal = %d, %v", len(list2), err)
	}
	for _, e := range list2 {
		if e.ID == list[0].ID {
			t.Fatal("stale entry listed")
		}
	}
	all, _ := store.List(0)
	if len(all) != len(ids)-1 {
		t.Fatalf("List(0) = %d, want %d", len(all), len(ids)-1)
	}
}

func TestRED_Session_PrincipalPromptsRescannedAfterSecretRegistered(t *testing.T) {
	redact.ResetSecrets()
	t.Cleanup(redact.ResetSecrets)
	store, err := NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	secret := "late-registered-secret-value-0123456789"
	p := "please use " + secret
	sess, err := store.Create([]Message{{Role: "user", Content: "c", PrincipalPrompt: &p}}, "m", "task")
	if err != nil {
		t.Fatal(err)
	}
	// The value becomes a known secret only after the first save persisted
	// the prompt verbatim. The next save must scan the unchanged prompt again.
	redact.RegisterSecret(secret)
	sess.Messages = append(sess.Messages, Message{Role: "assistant", Content: "a"})
	if err := store.SaveNoIndex(sess); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Messages[0].PrincipalPrompt == nil || strings.Contains(*got.Messages[0].PrincipalPrompt, secret) {
		t.Fatalf("prompt still carries a secret registered after the previous save: %q", *got.Messages[0].PrincipalPrompt)
	}
}
