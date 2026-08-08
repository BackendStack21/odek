package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/events"
)

func TestParseRunFlags_EventsJSONL(t *testing.T) {
	f, err := parseRunFlags([]string{"--events-jsonl", "/tmp/odek-events.jsonl", "do the thing"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.EventsJSONL != "/tmp/odek-events.jsonl" {
		t.Errorf("EventsJSONL = %q, want /tmp/odek-events.jsonl", f.EventsJSONL)
	}
	if f.Task != "do the thing" {
		t.Errorf("Task = %q, want %q", f.Task, "do the thing")
	}

	// Missing value is a hard error.
	if _, err := parseRunFlags([]string{"--events-jsonl"}); err == nil {
		t.Error("expected error for --events-jsonl without a value")
	}

	// Unset by default.
	f, err = parseRunFlags([]string{"do the thing"})
	if err != nil {
		t.Fatalf("parseRunFlags error: %v", err)
	}
	if f.EventsJSONL != "" {
		t.Errorf("EventsJSONL default = %q, want empty", f.EventsJSONL)
	}
}

func TestEventsJSONL_SinkEndToEnd(t *testing.T) {
	// The exact handler shape wired into odek.Config.EventHandler by the run
	// command: emitter → handler → JSONL sink, drained on Close.
	dir := t.TempDir()
	path := dir + "/events.jsonl"

	sink, err := events.OpenJSONLSink(path)
	if err != nil {
		t.Fatalf("OpenJSONLSink: %v", err)
	}
	em := events.NewEmitter(func(ev events.Event) {
		if err := sink.Write(ev); err != nil {
			t.Errorf("sink write: %v", err)
		}
	}, events.NewRunID())
	em.Emit(events.Event{Type: events.TypeRunStarted, Data: map[string]any{"model": "test-model"}})
	em.SetSessionID("sess-1")
	em.Emit(events.Event{Type: events.TypeSessionSaved, Data: map[string]any{"message_count": 5}})
	em.Emit(events.Event{Type: events.TypeRunCompleted, Data: map[string]any{"duration_ms": 3}})
	em.Close()
	if err := sink.Close(); err != nil {
		t.Fatalf("sink close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	for i, line := range lines {
		var ev events.Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d not parseable: %v", i+1, err)
		}
		if ev.Schema != events.Schema {
			t.Errorf("line %d schema = %q", i+1, ev.Schema)
		}
	}
}
