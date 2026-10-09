package events

import (
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// Redaction of large payloads must not run under the emitter mutex, so
// concurrent emitters scale across cores.
func TestRED_Events_RedactionRunsOutsideLock(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 4 {
		t.Skip("needs >= 4 CPUs")
	}
	payload := strings.Repeat("abc sk-0123456789abcdef0123456789abcdef text ", 4000)
	mk := func() Event {
		return Event{Type: TypeToolCallCompleted, Data: map[string]any{"out": payload}}
	}
	handler, _ := collect()
	e := NewEmitter(handler, "run")
	defer e.Close()

	const n = 8
	start := time.Now()
	for i := 0; i < n; i++ {
		e.Emit(mk())
	}
	serial := time.Since(start)

	start = time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.Emit(mk())
		}()
	}
	wg.Wait()
	parallel := time.Since(start)
	if parallel > serial*3/4 {
		t.Fatalf("concurrent Emit did not scale: serial=%v parallel=%v", serial, parallel)
	}
}
