package embedding

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/go-vector/pkg/vector"
)

func TestCosineEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		a, b vector.Vector
		want float32
	}{
		{"identical", vector.Vector{1, 2, 3}, vector.Vector{1, 2, 3}, 1},
		{"orthogonal", vector.Vector{1, 0}, vector.Vector{0, 1}, 0},
		{"opposite", vector.Vector{1, 0}, vector.Vector{-1, 0}, -1},
		{"mismatched length", vector.Vector{1, 2}, vector.Vector{1, 2, 3}, 0},
		{"empty", vector.Vector{}, vector.Vector{}, 0},
		{"zero norm a", vector.Vector{0, 0}, vector.Vector{1, 1}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Cosine(tt.a, tt.b)
			if math.Abs(float64(got-tt.want)) > 1e-6 {
				t.Errorf("Cosine(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestCosineNaNInfClampedToZero(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	if got := Cosine(vector.Vector{nan, 1}, vector.Vector{1, 1}); got != 0 {
		t.Errorf("Cosine with NaN component = %v, want 0 (clamped)", got)
	}
	if got := Cosine(vector.Vector{inf, 1}, vector.Vector{1, 1}); got != 0 {
		t.Errorf("Cosine with Inf component = %v, want 0 (clamped)", got)
	}
}

func TestNewRPDefaultsDims(t *testing.T) {
	// dims <= 0 must fall back to DefaultRPDim so the fingerprint is stable.
	for _, dims := range []int{0, -1, -100} {
		if got := NewRP(dims).Fingerprint(); got != "rp/256" {
			t.Errorf("NewRP(%d).Fingerprint() = %q, want rp/256", dims, got)
		}
	}
	if got := NewRP(128).Fingerprint(); got != "rp/128" {
		t.Errorf("NewRP(128).Fingerprint() = %q, want rp/128", got)
	}
}

func TestNewHTTPAppliesAPIKeyAndTimeout(t *testing.T) {
	t.Setenv("ODEK_EDGE_KEY", "sk-edge")
	emb := New(&Config{
		Provider:       "http",
		BaseURL:        "http://localhost:9/v1",
		Model:          "m",
		APIKey:         "${ODEK_EDGE_KEY}",
		Dims:           512,
		TimeoutSeconds: 3,
	}, 64)
	if _, ok := emb.(*httpTextEmbedder); !ok {
		t.Fatalf("New(http) = %T, want *httpTextEmbedder", emb)
	}
	// Dims flow into the fingerprint so a model dimensionality change rebuilds.
	if got := emb.Fingerprint(); got != "http/m/512" {
		t.Errorf("fingerprint = %q, want http/m/512", got)
	}
}

// TestHTTPEmbedderStateNoOps: the stateless HTTP backend persists nothing and
// always reports usable after a load — the no-op SaveState/LoadState contract
// other subsystems rely on for "loadState always true when stateless".
func TestHTTPEmbedderStateNoOps(t *testing.T) {
	emb := New(&Config{Provider: "http", BaseURL: "http://localhost:9/v1", Model: "m"}, 64)
	dir := t.TempDir()
	path := filepath.Join(dir, "state.gob")

	emb.SaveState(path) // must write nothing
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("HTTP SaveState wrote a file (%v); it must be a no-op", err)
	}
	if !emb.LoadState(path) {
		t.Error("HTTP LoadState must return true (stateless backend is always usable)")
	}
}

// TestRPSaveStateMissingDirIsSafe: SaveState swallows a failed write (bad path)
// without panicking, leaving no stray temp file behavior to the caller.
func TestRPSaveStateMissingDirIsSafe(t *testing.T) {
	emb := NewRP(32)
	_ = emb.Fit([]string{"alpha beta", "gamma delta"})
	// A path under a nonexistent directory makes the underlying save fail.
	emb.SaveState(filepath.Join(t.TempDir(), "no-such-dir", "state.gob"))
	// LoadState from a missing file returns false rather than erroring.
	if NewRP(32).LoadState(filepath.Join(t.TempDir(), "absent.gob")) {
		t.Error("LoadState of a missing file should return false")
	}
}
