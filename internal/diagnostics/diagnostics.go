// Package diagnostics routes application failures to an optional process-wide
// observer. It never captures stderr, error text, paths, or request content.
package diagnostics

import (
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/events"
)

var observer struct {
	sync.RWMutex
	handler func(events.Event)
}

// Install registers a non-blocking observer and returns a function restoring
// the previous observer. The process owner installs it before loading config
// and removes it after its services stop. Library use is opt-in.
func Install(handler func(events.Event)) func() {
	observer.Lock()
	previous := observer.handler
	observer.handler = handler
	observer.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			observer.Lock()
			observer.handler = previous
			observer.Unlock()
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
	handler := observer.handler
	observer.RUnlock()
	if handler != nil {
		defer func() { _ = recover() }()
		handler(ev)
	}
}
