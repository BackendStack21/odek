package main

import (
	"strings"
	"testing"
)

func TestRED_ClaimMCPToolNameRejectsDuplicates(t *testing.T) {
	seen := map[string]struct{}{}
	if err := claimMCPToolName(seen, "a", "t", "a__t"); err != nil {
		t.Fatal(err)
	}
	err := claimMCPToolName(seen, "b", "t", "a__t")
	if err == nil || !strings.Contains(err.Error(), "already uses") {
		t.Fatalf("duplicate registered name accepted: %v", err)
	}
}
