package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A release whose checksums.txt carries no Sigstore signature must not be
// installed: a matching checksum from the same release proves nothing when
// the release itself was tampered with.
func TestRED_PerformUpgrade_RefusesUnsignedChecksums(t *testing.T) {
	newBin := []byte("#!/bin/sh\necho attacker\n")
	srv := fakeReleaseServer(t, "v9.9.9", newBin, "")

	exe := filepath.Join(t.TempDir(), "odek")
	if err := os.WriteFile(exe, []byte("old binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	_, err := performUpgrade(context.Background(), srv.Client(), srv.URL, exe, "v1.0.0", upgradeOptions{}, &out)
	if err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected a signature refusal, got err=%v", err)
	}
	content, _ := os.ReadFile(exe)
	if string(content) != "old binary" {
		t.Fatalf("unsigned release was installed: %q", content)
	}
}
