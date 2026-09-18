package extended

import (
	"sync"
	"testing"
)

func TestUserModelSaveSerializesStateEncoding(t *testing.T) {
	u := NewUserModelWithStore(t.TempDir(), nil, DefaultConfig())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if err := u.applyDiff(t.Context(), userStateDiff{Pending: []PendingReview{{Field: "style.tone", Value: "calm"}}}); err != nil {
					t.Error(err)
					return
				}
				if err := u.Save(); err != nil {
					t.Error(err)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestUserModelStateIsDeepCopy(t *testing.T) {
	u := NewUserModelWithStore(t.TempDir(), nil, DefaultConfig())
	_ = u.applyDiff(t.Context(), userStateDiff{
		Technical:   &TechnicalState{Languages: []string{"go"}, Patterns: []string{"p"}, Tools: []string{"tool"}},
		Interaction: &InteractionPatterns{CommonOpeners: []string{"hello"}},
		Pending:     []PendingReview{{ID: "p1", Field: "style.tone", Value: "calm"}},
	})
	s := u.State()
	s.Technical.Languages[0] = "mutated"
	s.Technical.Patterns[0] = "mutated"
	s.Technical.Tools[0] = "mutated"
	s.InteractionPatterns.CommonOpeners[0] = "mutated"
	s.PendingReview[0].Value = "mutated"
	got := u.State()
	if got.Technical.Languages[0] != "go" || got.Technical.Patterns[0] != "p" || got.Technical.Tools[0] != "tool" || got.InteractionPatterns.CommonOpeners[0] != "hello" || got.PendingReview[0].Value != "calm" {
		t.Fatalf("State exposed mutable backing arrays: %#v", got)
	}
}
