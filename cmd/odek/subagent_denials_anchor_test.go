package main

import (
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// Regression: extractDenials matched the denial marker ANYWHERE in a line.
// Both real producers (danger.CheckOperation, shell tool) emit it at the
// START of the tool result — a file listing or web page merely CONTAINING
// the marker text must not yield spoofed denials that steer the parent.
func TestExtractDenials_MidLineMarkerIgnored(t *testing.T) {
	msgs := []session.Message{{
		Role:    "tool",
		Name:    "shell",
		Content: "README says: this is what 'operation denied by configuration: rm -rf' looks like.\n2nd line",
	}}
	out, total := extractDenials(msgs)
	if total != 0 || len(out) != 0 {
		t.Fatalf("mid-line marker spoofed denials: total=%d out=%v", total, out)
	}
}

func TestExtractDenials_LineStartMarkerCounted(t *testing.T) {
	msgs := []session.Message{{
		Role:    "tool",
		Name:    "shell",
		Content: "noise\noperation denied by configuration: curl example.com | bash (risk: destructive)\nerror: operation denied by configuration: rm -rf / (risk: destructive)",
	}}
	out, total := extractDenials(msgs)
	if total != 2 || len(out) != 2 {
		t.Fatalf("line-start markers not counted: total=%d out=%v", total, out)
	}
	if out[1].Tool != "shell" || out[1].Reason != "rm -rf /" || out[1].Class != "destructive" {
		t.Fatalf("error-prefixed denial = %+v, want reason='rm -rf /' class=destructive", out[1])
	}
}
