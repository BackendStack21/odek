package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// patch documents "Returns a unified diff showing changes". Editing a line
// other than the first must show that line, not the (unchanged) first line
// of the file.
func TestRED_PatchDiffShowsChangedLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte("package main\n\nfunc a() int { return 1 }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := &patchTool{}
	result := callJSON(t, tool, fmt.Sprintf(`{"path":%q,"old_string":"return 1","new_string":"return 2"}`, path))
	var r struct {
		Success bool   `json:"success"`
		Diff    string `json:"diff"`
	}
	mustUnmarshal(t, result, &r)
	if !r.Success {
		t.Fatalf("patch failed: %s", result)
	}
	diff := unwrapUntrusted(r.Diff)
	if !strings.Contains(diff, "-func a() int { return 1 }") || !strings.Contains(diff, "+func a() int { return 2 }") {
		t.Fatalf("diff does not show the change (return 1 -> return 2):\n%s", diff)
	}
	if strings.Contains(diff, "-package main") {
		t.Fatalf("unchanged first line reported as removed:\n%s", diff)
	}
	if !strings.Contains(diff, "@@ -3 +3 @@") && !strings.Contains(diff, "@@ -2,2 +2,2 @@") && !strings.Contains(diff, "@@ -1,3 +1,3 @@") {
		t.Fatalf("hunk header does not locate the edited line:\n%s", diff)
	}
}

// The patch diff stays bounded however many lines a replace_all touches.
func TestPatchDiffBoundedTools(t *testing.T) {
	var a, b strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&a, "line %d x\n", i)
		fmt.Fprintf(&b, "line %d y\n", i)
	}
	d := patchUnifiedDiff("f", a.String(), b.String())
	if len(d) > 16*1024 {
		t.Fatalf("diff unbounded: %d bytes", len(d))
	}
	if !strings.Contains(d, "more lines") {
		t.Fatalf("elision marker missing:\n%s", d)
	}
	// Pure insertion and pure deletion render without panicking.
	if d := patchUnifiedDiff("f", "a\nb\n", "a\nX\nb\n"); !strings.Contains(d, "+X") {
		t.Fatalf("insertion diff:\n%s", d)
	}
	if d := patchUnifiedDiff("f", "a\nX\nb\n", "a\nb\n"); !strings.Contains(d, "-X") {
		t.Fatalf("deletion diff:\n%s", d)
	}
}

// config_view receives the raw tool-call arguments JSON from the loop
// ({"section":"security"}), exactly like every other tool. It must honour
// the section parameter its own schema advertises.
func TestRED_ConfigViewJSONArgs(t *testing.T) {
	tool := &configViewTool{view: buildConfigView(introspectionFixture())}

	out, err := tool.Call(`{"section":"security"}`)
	if err != nil {
		t.Fatalf("config_view with schema-shaped args failed: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := m["sandbox"]; !ok {
		t.Errorf("security section missing sandbox: %s", out)
	}
	if _, ok := m["model"]; ok {
		t.Errorf("security section must not carry model: %s", out)
	}

	// A no-argument call is sent as "{}" by most providers; it is the full view.
	full, err := tool.Call(`{}`)
	if err != nil {
		t.Fatalf("config_view with empty-object args failed: %v", err)
	}
	all, _ := tool.Call("all")
	if full != all {
		t.Errorf("{} must render the full view")
	}
	// {"section":"all"} and whitespace-padded JSON behave the same.
	if got, err := tool.Call(" {\"section\":\"all\"} "); err != nil || got != all {
		t.Errorf(`{"section":"all"} = %v, %v`, got, err)
	}
	// Unknown section in JSON form still errors.
	if _, err := tool.Call(`{"section":"nope"}`); err == nil {
		t.Errorf("unknown section must error")
	}
	// Malformed JSON errors rather than being treated as a section name.
	if _, err := tool.Call(`{"section":`); err == nil {
		t.Errorf("malformed args must error")
	}
}
