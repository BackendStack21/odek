package loop

import (
	"testing"

	"github.com/BackendStack21/odek/internal/llm"
)

// insertions (skill/episode/extended-memory context) and the trim warning
// must land immediately before the user's REAL message — bg-notice user
// messages are synthetic (Name=bg-*) and must be skipped, exactly like
// lastUserMessage does. With a notice drained after the task, the plain
// scan placed injections BETWEEN the task and its notice.
func TestInsertionIndex_SkipsBgNotices(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "the real task"},
		{Role: "user", Content: "job finished", Name: "bg-notice"},
	}
	if got := insertionIndexBeforeLatestUser(msgs); got != 1 {
		t.Fatalf("insertionIndexBeforeLatestUser = %d, want 1 (before the real task, skipping the bg-notice)", got)
	}
}

func TestUpsertTrimWarning_SkipsBgNotices(t *testing.T) {
	msgs := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "the real task"},
		{Role: "user", Content: "job finished", Name: "bg-notice"},
	}
	out := upsertTrimWarning(msgs, "[Context trimmed: x]")
	warnIdx, taskIdx, noticeIdx := -1, -1, -1
	for i, m := range out {
		if m.Role == "system" && len(m.Content) > 0 && m.Content[:1] == "[" && i > 0 {
			warnIdx = i
		}
		if m.Content == "the real task" {
			taskIdx = i
		}
		if m.Name == "bg-notice" {
			noticeIdx = i
		}
	}
	if warnIdx == -1 {
		t.Fatal("trim warning not inserted")
	}
	if !(warnIdx < taskIdx && taskIdx < noticeIdx) {
		t.Fatalf("ordering: warning=%d task=%d notice=%d — warning must sit before the real task, not before the notice", warnIdx, taskIdx, noticeIdx)
	}
}
