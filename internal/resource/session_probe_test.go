package resource

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRED_Resource_SessionSearchDoesNotDecodeHistory(t *testing.T) {
	dir := t.TempDir()
	pad := strings.Repeat("x", 900<<10)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("20260101-%032x", i)
		body := fmt.Sprintf(`{"revision":1,"id":%q,"messages":[{"content":%q}]}`, id, pad)
		if err := os.WriteFile(filepath.Join(dir, id+".json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r := NewSessionResolver(dir)
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	res, err := r.Search(context.Background(), "", 10)
	runtime.ReadMemStats(&after)
	if err != nil || len(res) != 5 {
		t.Fatalf("res=%d err=%v", len(res), err)
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 1<<20 {
		t.Fatalf("search allocated %d bytes for 4.5MB of history", got)
	}
}

func TestProbeSessionID(t *testing.T) {
	cases := []struct {
		in, id string
		ok     bool
	}{
		{`{"id":"a"}`, "a", true},
		{`{"revision":3,"generation":"g","id":"b","messages":[1,2]}`, "b", true},
		{`{"messages":[]}`, "", false},
		{`[1]`, "", false},
		{`{"id":5}`, "", false},
		{``, "", false},
		{`{"a":}`, "", false},
	}
	for _, c := range cases {
		id, ok := probeSessionID(strings.NewReader(c.in))
		if id != c.id || ok != c.ok {
			t.Errorf("%q: got %q,%v", c.in, id, ok)
		}
	}
}
