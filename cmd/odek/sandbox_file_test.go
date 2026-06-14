package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostToContainerPath(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	cases := []struct {
		name    string
		host    string
		want    string
		wantErr bool
	}{
		{"relative file", "foo.txt", "/workspace/foo.txt", false},
		{"relative nested", "src/main.go", "/workspace/src/main.go", false},
		{"absolute under cwd", filepath.Join(dir, "bar.txt"), "/workspace/bar.txt", false},
		{"cwd itself", dir, "/workspace", false},
		{"outside cwd", "/etc/passwd", "", true},
		{"traversal", "../foo.txt", "", true},
		{"absolute traversal", filepath.Join(dir, "..", "foo.txt"), "", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := hostToContainerPath(tc.host)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("hostToContainerPath(%q) = %q, want %q", tc.host, got, tc.want)
			}
		})
	}
}

func TestHostToContainerPath_RejectsEmpty(t *testing.T) {
	_, err := hostToContainerPath("")
	if err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestSandboxWriteFile_RequiresContainerName(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	// Empty container name will cause docker cp to fail quickly; we just check
	// that the path translation happens and the command errors.
	err := sandboxWriteFile("", "test.txt", []byte("hello"), 0644)
	if err == nil {
		t.Fatal("expected error for empty container name")
	}
	if !strings.Contains(err.Error(), "docker") && !strings.Contains(err.Error(), "container") {
		t.Errorf("error = %q, want docker/container mention", err)
	}
}

// TestSandboxReadonly_RoutesWriteFileThroughContainer verifies that when a
// sandbox container name is set on write_file, the tool attempts to route the
// write through the container instead of touching the host filesystem. A
// non-existent container makes the docker command fail, but the important
// precondition is that no file appears on the host.
func TestSandboxReadonly_RoutesWriteFileThroughContainer(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	tool := &writeFileTool{restrictToCWD: true, containerName: "odek-test-nonexistent"}
	out, _ := tool.Call(`{"path":"routed.txt","content":"should not appear on host"}`)

	if !strings.Contains(out, "via sandbox") {
		t.Fatalf("expected write_file to route through sandbox, got: %s", out)
	}

	target := filepath.Join(dir, "routed.txt")
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("write_file created a host file despite sandbox routing: %s", target)
	}

	// Sanity-check the JSON envelope is an error, not a success.
	var res writeFileResult
	if err := json.Unmarshal([]byte(out), &res); err == nil && res.Success {
		t.Fatalf("expected failure for non-existent container, got success: %+v", res)
	}
}
