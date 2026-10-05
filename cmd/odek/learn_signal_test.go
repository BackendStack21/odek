package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/llmclient"
)

// The serve surface must translate provider learn events into agent_signal
// frames with a human-readable detail line — the client-visible form of
// "your provider silently stopped streaming".
func TestLearnSignalFrame_Shape(t *testing.T) {
	ev := llmclient.LearnEvent{
		Kind:     llmclient.LearnBuffered,
		Provider: "litellm",
	}
	frame := learnSignalFrame(ev)
	if frame["type"] != "agent_signal" {
		t.Errorf("type = %v, want agent_signal", frame["type"])
	}
	if frame["event"] != "provider_learn_fallback" {
		t.Errorf("event = %v, want provider_learn_fallback", frame["event"])
	}
	if frame["kind"] != string(llmclient.LearnBuffered) {
		t.Errorf("kind = %v, want %q", frame["kind"], llmclient.LearnBuffered)
	}
	if frame["provider"] != "litellm" {
		t.Errorf("provider = %v, want litellm", frame["provider"])
	}
	detail, _ := frame["detail"].(string)
	if detail == "" {
		t.Fatal("detail must be a non-empty human-readable line")
	}
	if !strings.Contains(detail, "litellm") || !strings.Contains(detail, "buffered") {
		t.Errorf("detail %q must name the provider and the fallback", detail)
	}
}

// Status and message from a 400-class fallback must reach the frame.
func TestLearnSignalFrame_WithStatusAndMessage(t *testing.T) {
	ev := llmclient.LearnEvent{
		Kind:     llmclient.LearnDropStreamOpts,
		Provider: "gw",
		Status:   400,
		Message:  "stream_options is not supported",
	}
	frame := learnSignalFrame(ev)
	detail, _ := frame["detail"].(string)
	if !strings.Contains(detail, "400") || !strings.Contains(detail, "stream_options") {
		t.Errorf("detail %q must carry status and provider message", detail)
	}
}
