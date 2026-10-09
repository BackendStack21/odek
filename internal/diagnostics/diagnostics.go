// Package diagnostics routes application failures to an optional process-wide
// observer. It never captures stderr, error text, paths, or request content.
package diagnostics

import (
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/events"
)

// installed is one registered observer. Installs form a chain through prev, so
// the observer that was active before this one can be found again on removal.
type installed struct {
	handler func(events.Event)
	prev    *installed
	removed bool
}

var observer struct {
	sync.RWMutex
	top *installed
}

// Install registers a non-blocking observer and returns a function removing
// it. Removal restores the most recent observer that is still installed, so
// observers may be removed in any order: removing an observer that is not the
// active one only marks it, and it is never reactivated later. The process
// owner installs it before loading config and removes it after its services
// stop. Library use is opt-in.
func Install(handler func(events.Event)) func() {
	entry := &installed{handler: handler}
	observer.Lock()
	entry.prev = observer.top
	observer.top = entry
	observer.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			observer.Lock()
			defer observer.Unlock()
			entry.removed = true
			if observer.top != entry {
				return
			}
			prev := entry.prev
			for prev != nil && prev.removed {
				prev = prev.prev
			}
			observer.top = prev
		})
	}
}

// Report records an operation failure. component and operation must be static
// program labels, never user input. A session ID is optional for global work.
// Callers retain responsibility for their existing user-facing diagnostics.
func Report(component, operation, sessionID string, err error) {
	if err == nil {
		return
	}
	Emit(Failure(component, operation, sessionID, err))
}

func Failure(component, operation, sessionID string, err error) events.Event {
	data := events.ErrorData(err)
	data["component"], data["operation"] = component, operation
	return events.Event{Type: "operation_failed", SessionID: sessionID, Data: data}
}

// Warning records a known degraded state, including validation fallbacks.
func Warning(component, operation string, err error) {
	ev := Failure(component, operation, "", err)
	ev.Type = "operation_warning"
	Emit(ev)
}

func Emit(ev events.Event) {
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now().UTC()
	}
	observer.RLock()
	var handler func(events.Event)
	if observer.top != nil {
		handler = observer.top.handler
	}
	observer.RUnlock()
	if handler != nil {
		defer func() { _ = recover() }()
		handler(ev)
	}
}
