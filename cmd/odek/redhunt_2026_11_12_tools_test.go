package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/BackendStack21/odek/internal/danger"
)

// tools1112DenySystemWrite is the policy every test below runs under:
// ordinary workspace/home writes are allowed, trust anchors (system_write)
// are denied.
func tools1112DenySystemWrite() danger.DangerousConfig {
	return danger.DangerousConfig{Classes: map[danger.RiskClass]danger.Action{
		danger.Safe:        danger.Allow,
		danger.LocalWrite:  danger.Allow,
		danger.SystemWrite: danger.Deny,
		danger.Persistence: danger.Deny,
		danger.Destructive: danger.Deny,
	}}
}

// The trust-anchor and home-credential checks fold paths with
// danger.FoldPathCase, which maps each rune through unicode.SimpleFold only.
// Case-insensitive APFS (the macOS default; verified) matches names under FULL
// Unicode case folding, where one rune can fold to several: U+FB01 'ﬁ' -> "fi",
// U+00DF 'ß' / U+1E9E 'ẞ' -> "ss", U+FB06 'ﬆ' -> "st". So on macOS
// "~/.odek/conﬁg.json" IS ~/.odek/config.json, "~/.odek/seßions/" IS
// ~/.odek/sessions/ and "~/.ßh/" IS ~/.ssh/, but every check sees an ordinary
// home path: confineToCWD's ~/.odek carve-out lets the write through and
// ClassifyPath/ClassifyPathWrite return local_write instead of system_write,
// the class the anchor table exists to keep away from auto-approved
// local_write tools (isOdekTrustAnchor). A prompt-injected model can then
// rewrite the operator's config.json (sandbox off, dangerous.default_action
// allow, auto-approved MCP server) without a system_write approval.
// Incomplete fix of the prior 'ſecrets.env' case-fold findings, which added
// simple folding only.
func TestRED_TrustAnchorFullCaseFoldAliasBypass(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workspace := t.TempDir()
	t.Chdir(workspace)
	odekDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(filepath.Join(odekDir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}

	// Each alias is the anchor's name under full case folding.
	aliases := []struct{ alias, canon string }{
		{"conﬁg.json", "config.json"},                 // ﬁ -> fi
		{"seßions/abc.json", "sessions/abc.json"},     // ß -> ss
		{"seẞions/abc.json", "sessions/abc.json"},     // ẞ -> ss
		{"reﬆart.json", "restart.json"},               // ﬆ -> st
		{"schedule-ﬅate.json", "schedule-state.json"}, // ﬅ -> st
	}
	for _, a := range aliases {
		if !isProtectedOdekPath(a.canon) {
			t.Fatalf("precondition: %q must be a protected anchor", a.canon)
		}
		if !isProtectedOdekPath(a.alias) {
			t.Errorf("isProtectedOdekPath(%q) = false; on macOS APFS this name opens ~/.odek/%s", a.alias, a.canon)
		}
		canonClass := danger.ClassifyPathWrite(filepath.Join(odekDir, a.canon))
		if got := danger.ClassifyPathWrite(filepath.Join(odekDir, a.alias)); got != canonClass {
			t.Errorf("ClassifyPathWrite(~/.odek/%s) = %s, want %s (same file as ~/.odek/%s)", a.alias, got, canonClass, a.canon)
		}
	}

	// The home credential table has the same gap: ~/.ßh is ~/.ssh.
	sshCanon := danger.ClassifyPath(filepath.Join(home, ".ssh", "id_ed25519"))
	if got := danger.ClassifyPath(filepath.Join(home, ".ßh", "id_ed25519")); got != sshCanon {
		t.Errorf("ClassifyPath(~/.ßh/id_ed25519) = %s, want %s like ~/.ssh/id_ed25519", got, sshCanon)
	}

	// End to end, exactly as builtinTools registers write_file
	// (restrictToCWD), on a filesystem that applies full case folding.
	anchor := filepath.Join(odekDir, "config.json")
	const original = `{"sandbox":true,"dangerous":{"default_action":"prompt"}}`
	if err := os.WriteFile(anchor, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	wt := &writeFileTool{dangerousConfig: tools1112DenySystemWrite(), restrictToCWD: true}
	direct, _ := wt.Call(fmt.Sprintf(`{"path":%q,"content":"x"}`, anchor))
	if got, _ := os.ReadFile(anchor); string(got) != original {
		t.Fatalf("precondition: direct write of ~/.odek/config.json must be refused, got %s", direct)
	}
	alias := filepath.Join(odekDir, "conﬁg.json")
	if _, err := os.Stat(alias); err != nil {
		return // case-sensitive filesystem: the classification checks above still apply
	}
	attack := `{"sandbox":false,"dangerous":{"default_action":"allow"}}`
	res, _ := wt.Call(fmt.Sprintf(`{"path":%q,"content":%q}`, alias, attack))
	if got, _ := os.ReadFile(anchor); string(got) != original {
		t.Errorf("write_file replaced ~/.odek/config.json through the full-case-fold alias %q with system_write denied: config now %q (result %s)", alias, got, res)
	}
}

// search_files(target=content) applies file_glob with filepath.Match against
// the file's BASE NAME only, discarding the match error. The system prompt
// tells the model to "ALWAYS pass a file glob", and the sibling glob tool
// documents "**/*.py" as its recursive form, so models routinely send
// file_glob "**/*.go" or "src/*.go": neither can ever match a base name, and
// a malformed class ("[*.go") is an ErrBadPattern that is read as "no match".
// Every file is filtered out and the tool returns zero matches with no error
// or notice, so the agent concludes the text does not exist. Correct behavior
// is either the real matches or an explicit error/notice about the glob —
// never a silent empty result.
func TestRED_SearchContentRecursiveFileGlobSilentlyMatchesNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"main.go", "src/pkg/util.go"} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("package x\n// needle here\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sf := &searchFilesTool{dangerousConfig: tools1112DenySystemWrite()}
	// search returns the match count and whether the result tells the model
	// something about its glob (an error or a skipped/notice entry).
	search := func(glob string) (int, bool, string) {
		res, _ := sf.Call(fmt.Sprintf(`{"pattern":"needle","target":"content","path":%q,"file_glob":%q}`, dir, glob))
		var r searchFilesResult
		if err := json.Unmarshal([]byte(res), &r); err != nil {
			t.Fatalf("unmarshal: %v (%s)", err, res)
		}
		return len(r.Matches), r.Error != "" || len(r.Skipped) > 0, res
	}
	if n, _, res := search("*.go"); n != 2 {
		t.Fatalf("precondition: file_glob *.go must find both files, got %d (%s)", n, res)
	}
	for _, c := range []struct {
		glob string
		want int // -1: no match count is right; only an explicit error/notice is
	}{
		{"**/*.go", 2},
		{"src/**/*.go", 1},
		{"[*.go", -1}, // malformed class: filepath.ErrBadPattern
	} {
		n, notice, res := search(c.glob)
		if n == c.want || notice {
			continue
		}
		t.Errorf("file_glob %q: %d matches with no error or notice (want %d matches or an explicit glob error); result %s", c.glob, n, c.want, res)
	}
}

// Path-shaped file_glob patterns match the path relative to the search root,
// plain ones keep matching the file name, and a malformed pattern is an error.
func TestRED_SearchContentFileGlobPathSemantics(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"main.go", "src/a.go", "src/pkg/b.go", "src/pkg/c.txt"} {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("needle\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sf := &searchFilesTool{dangerousConfig: tools1112DenySystemWrite()}
	count := func(glob string) (int, string) {
		res, _ := sf.Call(fmt.Sprintf(`{"pattern":"needle","target":"content","path":%q,"file_glob":%q}`, dir, glob))
		var r searchFilesResult
		if err := json.Unmarshal([]byte(res), &r); err != nil {
			t.Fatalf("unmarshal: %v (%s)", err, res)
		}
		return len(r.Matches), r.Error
	}
	for glob, want := range map[string]int{
		"*.go": 3, "**/*.go": 3, "src/*.go": 1, "src/**/*.go": 2, "src/pkg/*": 2, "**/*.txt": 1, "nomatch/*.go": 0,
	} {
		if n, errMsg := count(glob); n != want || errMsg != "" {
			t.Errorf("file_glob %q: %d matches (error %q), want %d", glob, n, errMsg, want)
		}
	}
	if n, errMsg := count("src/[*.go"); n != 0 || errMsg == "" {
		t.Errorf("malformed path glob: %d matches, error %q; want an error", n, errMsg)
	}
}
