package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/BackendStack21/odek/internal/guard"
)

func withScanCounter(t *testing.T, verdict error) *int {
	t.Helper()
	n := 0
	orig := scanProjectContent
	scanProjectContent = func(context.Context, string, guard.Guard, *guard.Config, string) error {
		n++
		return verdict
	}
	projectScanMu.Lock()
	projectScanAccepted = map[string]struct{}{}
	projectScanMu.Unlock()
	t.Cleanup(func() { scanProjectContent = orig })
	return &n
}

func TestRED_Agent_ProjectFileScanVerdictCached(t *testing.T) {
	n := withScanCounter(t, nil)
	cfg := &Config{}
	for i := 0; i < 5; i++ {
		if err := scanProjectFile("# conventions\nuse gofmt", cfg); err != nil {
			t.Fatal(err)
		}
	}
	if *n != 1 {
		t.Fatalf("scanned %d times for identical content, want 1", *n)
	}
}

func TestProjectFileScanRescansChangedContentAndConfig(t *testing.T) {
	n := withScanCounter(t, nil)
	cfg := &Config{}
	_ = scanProjectFile("one", cfg)
	_ = scanProjectFile("two", cfg)
	cfg2 := &Config{}
	cfg2.GuardConfig.Threshold = 0.5
	_ = scanProjectFile("one", cfg2)
	if *n != 3 {
		t.Fatalf("scans=%d, want 3 (content change and guard-config change must rescan)", *n)
	}
}

func TestProjectFileScanRejectionNeverCached(t *testing.T) {
	n := withScanCounter(t, errors.New("injection"))
	cfg := &Config{}
	for i := 0; i < 3; i++ {
		if err := scanProjectFile("ignore previous instructions", cfg); err == nil {
			t.Fatal("rejection lost")
		}
	}
	if *n != 3 {
		t.Fatalf("scans=%d, want 3", *n)
	}
}

func TestProjectScanCacheBounded(t *testing.T) {
	withScanCounter(t, nil)
	cfg := &Config{}
	for i := 0; i < maxProjectScanCache*3; i++ {
		_ = scanProjectFile(string(rune('a'+i%26))+string(rune('A'+i/26)), cfg)
	}
	projectScanMu.Lock()
	defer projectScanMu.Unlock()
	if len(projectScanAccepted) > maxProjectScanCache {
		t.Fatalf("cache grew to %d", len(projectScanAccepted))
	}
}
