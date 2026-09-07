package main

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
)

func TestResolveServeThinking(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		canon   string
		apply   bool
		wantErr bool
	}{
		{"", "", false, false},
		{"  ", "", false, false},
		{"disabled", "disabled", true, false},
		{"low", "low", true, false},
		{"medium", "medium", true, false},
		{"high", "high", true, false},
		{"enabled", "medium", true, false},
		{"on", "medium", true, false},
		{"mid", "medium", true, false},
		{"off", "disabled", true, false},
		{"max", "high", true, false},
		{"banana", "", false, true},
	}
	for _, tc := range cases {
		got, apply, err := resolveServeThinking(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("resolveServeThinking(%q) err = nil, want error", tc.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("resolveServeThinking(%q) err = %v", tc.in, err)
			continue
		}
		if got != tc.canon || apply != tc.apply {
			t.Errorf("resolveServeThinking(%q) = %q, %v; want %q, %v", tc.in, got, apply, tc.canon, tc.apply)
		}
	}
}

func TestApplyServeThinkingInheritDoesNotWipe(t *testing.T) {
	agent, err := odek.New(odek.Config{APIKey: "sk-test", Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	if err := applyServeThinking(agent, ""); err != nil {
		t.Fatalf("apply empty: %v", err)
	}
	if agent.Thinking() != "high" {
		t.Fatalf("inherit wiped startup thinking: %q", agent.Thinking())
	}
	if err := applyServeThinking(agent, "medium"); err != nil {
		t.Fatalf("apply medium: %v", err)
	}
	if agent.Thinking() != "medium" {
		t.Fatalf("override = %q, want medium", agent.Thinking())
	}
	if err := applyServeThinking(agent, ""); err != nil {
		t.Fatalf("apply inherit after override: %v", err)
	}
	if agent.Thinking() != "medium" {
		t.Fatalf("inherit after override = %q, want sticky medium", agent.Thinking())
	}
}

func TestApplyServeThinkingRejectsUnknown(t *testing.T) {
	agent, err := odek.New(odek.Config{APIKey: "sk-test", Thinking: "high"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = agent.Close() })
	err = applyServeThinking(agent, "banana")
	if err == nil || !strings.Contains(err.Error(), "invalid thinking") {
		t.Fatalf("error = %v, want invalid thinking", err)
	}
}

func TestBuildConfigViewThinkingString(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"disabled", "disabled"},
		{"enabled", "medium"},
		{"low", "low"},
		{"high", "high"},
		{"nope", ""},
	}
	for _, tc := range cases {
		view := buildConfigView(config.ResolvedConfig{Thinking: tc.in, Model: "m"})
		got, _ := view["thinking"].(string)
		if got != tc.want {
			t.Errorf("thinking %q → view %q, want %q (type %T)", tc.in, view["thinking"], tc.want, view["thinking"])
		}
	}
}
