package odek

import (
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/session"
)

// TestNew_ExternalRefsValidated pins the public-API contract: an invalid
// Config.ExternalRefs entry is a clear startup error from New, before any
// session is persisted.
func TestNew_ExternalRefsValidated(t *testing.T) {
	_, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       "http://127.0.0.1:1",
		Model:         "test-model",
		NoProjectFile: true,
		ExternalRefs: []session.ExternalRef{
			{Kind: "CI-Run", URI: "https://ci.example.test/runs/1", CreatedBy: "ops"},
		},
	})
	if err == nil {
		t.Fatal("expected New to reject invalid external ref")
	}
	if !strings.Contains(err.Error(), "external_refs[0]") {
		t.Fatalf("error should index the offending ref, got: %v", err)
	}

	// A valid ref set constructs fine.
	agent, err := New(Config{
		APIKey:        "sk-test",
		BaseURL:       "http://127.0.0.1:1",
		Model:         "test-model",
		NoProjectFile: true,
		ExternalRefs: []session.ExternalRef{
			{Kind: "ci-run", URI: "https://ci.example.test/runs/1", CreatedBy: "ops"},
		},
	})
	if err != nil {
		t.Fatalf("New with valid refs: %v", err)
	}
	agent.Close()
}
