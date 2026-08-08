package main

import (
	"strings"
	"testing"
)

func TestParseExternalRefFlag(t *testing.T) {
	t.Run("primary form", func(t *testing.T) {
		ref, err := parseExternalRefFlag("kind=opaque_application_state,uri=app://workflow/123,created_by=example-app")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ref.Kind != "opaque_application_state" || ref.URI != "app://workflow/123" || ref.CreatedBy != "example-app" {
			t.Fatalf("unexpected ref: %+v", ref)
		}
		if ref.ReadOnly {
			t.Fatal("read_only should default to false")
		}
	})

	t.Run("shorthand kind=uri", func(t *testing.T) {
		ref, err := parseExternalRefFlag("ci-run=https://ci.example.test/runs/4821?x=1")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if ref.Kind != "ci-run" || ref.URI != "https://ci.example.test/runs/4821?x=1" {
			t.Fatalf("unexpected ref: %+v", ref)
		}
		if ref.CreatedBy != "cli" {
			t.Fatalf("shorthand created_by should default to cli, got %q", ref.CreatedBy)
		}
	})

	t.Run("read_only", func(t *testing.T) {
		ref, err := parseExternalRefFlag("kind=dashboard,uri=https://dash.example.test/b/1,created_by=ops,read_only=true")
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if !ref.ReadOnly {
			t.Fatal("expected read_only=true")
		}
	})

	errors := []struct {
		name string
		spec string
	}{
		{"no equals", "just-a-string"},
		{"empty", ""},
		{"unknown key", "kind=a,uri=app://x,created_by=c,bogus=1"},
		{"malformed pair", "kind=a,uri=app://x,noequals"},
		{"bad read_only", "kind=a,uri=app://x,created_by=c,read_only=maybe"},
		{"uppercase kind", "kind=CI-Run,uri=app://x,created_by=c"},
		{"empty kind shorthand", "=app://x"},
		{"empty uri shorthand", "ci-run="},
		{"control char in uri", "kind=a,uri=app://x\ny,created_by=c"},
		{"empty created_by", "kind=a,uri=app://x,created_by="},
	}
	for _, tc := range errors {
		t.Run("error/"+tc.name, func(t *testing.T) {
			if _, err := parseExternalRefFlag(tc.spec); err == nil {
				t.Fatalf("expected error for %q", tc.spec)
			} else if !strings.Contains(err.Error(), "--external-ref") {
				t.Fatalf("error should name the flag, got: %v", err)
			}
		})
	}
}

func TestParseRunFlagsExternalRef(t *testing.T) {
	f, err := parseRunFlags([]string{
		"--session",
		"--external-ref", "ci-run=https://ci.example.test/runs/1",
		"--external-ref", "kind=dashboard,uri=https://dash.example.test/b/2,created_by=ops",
		"do the thing",
	})
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	if len(f.ExternalRefs) != 2 {
		t.Fatalf("expected 2 raw refs, got %v", f.ExternalRefs)
	}
	refs, err := parseExternalRefFlags(f.ExternalRefs)
	if err != nil {
		t.Fatalf("parseExternalRefFlags: %v", err)
	}
	if refs[0].Kind != "ci-run" || refs[1].Kind != "dashboard" {
		t.Fatalf("unexpected refs: %+v", refs)
	}

	if _, err := parseRunFlags([]string{"--external-ref"}); err == nil {
		t.Fatal("expected error for missing --external-ref value")
	}
}

func TestParseContinueArgs(t *testing.T) {
	t.Run("plain task", func(t *testing.T) {
		id, refs, task, err := parseContinueArgs([]string{"fix", "the", "bug"})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if id != "" || len(refs) != 0 || task != "fix the bug" {
			t.Fatalf("unexpected: id=%q refs=%v task=%q", id, refs, task)
		}
	})

	t.Run("id and refs", func(t *testing.T) {
		id, refs, task, err := parseContinueArgs([]string{
			"--id", "abc",
			"--external-ref", "ci-run=https://ci.example.test/runs/1",
			"--external-ref", "kind=d,uri=app://x,created_by=c",
			"next step",
		})
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if id != "abc" || len(refs) != 2 || task != "next step" {
			t.Fatalf("unexpected: id=%q refs=%v task=%q", id, refs, task)
		}
	})

	t.Run("no task", func(t *testing.T) {
		if _, _, _, err := parseContinueArgs([]string{"--id", "abc"}); err == nil {
			t.Fatal("expected error when only flags given")
		}
		if _, _, _, err := parseContinueArgs(nil); err == nil {
			t.Fatal("expected error for empty args")
		}
	})
}
