package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readKeyFromInheritedFD and readFrameNonceFromInheritedFD take ownership of
// the descriptor named in their env var and close it. A test that names a
// descriptor still owned by an *os.File gets it closed twice: the file's
// finalizer later closes whatever the number has been reused for — in a
// parallel test often the directory fd os.RemoveAll is walking, which then
// fails with "unlinkat: bad file descriptor". Tests must hand over a dup.
func TestRED_FDHandoffTestsPassADuplicatedDescriptor(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	raw := regexp.MustCompile(`t\.Setenv\((keyFDEnvVar|frameFDEnvVar)[^\n]*\.Fd\(\)`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range raw.FindAllString(string(data), -1) {
			if !strings.Contains(m, "Dup") && !strings.Contains(m, "dupFD") {
				t.Errorf("%s hands an owned descriptor to the fd reader: %s (use dupFD)", f, m)
			}
		}
	}
}
