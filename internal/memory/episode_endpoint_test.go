package memory

import "testing"

func TestEpisodeIndexSwitchesHTTPEndpoints(t *testing.T) {
	for _, cold := range []bool{false, true} {
		name := "live"
		if cold {
			name = "persisted"
		}
		t.Run(name, func(t *testing.T) {
			resetEpIdxes()
			t.Cleanup(resetEpIdxes)
			first, firstRequests, _ := mockEmbedServer(t)
			second, secondRequests, secondTexts := mockEmbedServer(t)
			dir := t.TempDir()
			a := NewEpisodeStore(dir, nil)
			a.setEmbedderFactory(newHTTPEmbedderFactory(first))
			if err := a.Write("session-cats", "investigated feline behavior", 5); err != nil {
				t.Fatal(err)
			}
			if got, err := a.recallByVector("kitten care", 1); err != nil || len(got) != 1 {
				t.Fatalf("initial recall=%v err=%v", got, err)
			}
			priorRequests := firstRequests.Load()
			if cold {
				resetEpIdxes()
			}
			b := NewEpisodeStore(dir, nil)
			b.setEmbedderFactory(newHTTPEmbedderFactory(second))
			if got, err := b.recallByVector("kitten care", 1); err != nil || len(got) != 1 {
				t.Fatalf("changed endpoint recall=%v err=%v", got, err)
			}
			if firstRequests.Load() != priorRequests {
				t.Fatal("recall contacted the previous endpoint")
			}
			if secondRequests.Load() == 0 || secondTexts.Load() < 2 {
				t.Fatalf("new endpoint must embed both corpus and query: requests=%d texts=%d", secondRequests.Load(), secondTexts.Load())
			}
		})
	}
}
