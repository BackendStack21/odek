package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/logquery"
)

func TestLogsUsesConfiguredFileAndJSONOutput(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(home, "custom.jsonl")
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"logging":{"enabled":true,"file":"`+logPath+`","max_files":3}}`), 0600); err != nil {
		t.Fatal(err)
	}
	line := `{"schema":"odek.log/v1","timestamp":"2026-09-30T12:00:00Z","level":"info","event":"run_started","surface":"cli"}`
	if err := os.WriteFile(logPath, []byte(line+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(func() {
		if code := dispatch([]string{"logs", "--json"}); code != 0 {
			t.Errorf("logs exit = %d", code)
		}
	})
	if !strings.Contains(out, `"event":"run_started"`) {
		t.Fatalf("configured log was not queried: %s", out)
	}
	if _, err := os.Stat(filepath.Join(configDir, "runtime.log")); !os.IsNotExist(err) {
		t.Fatalf("unexpected default log created: %v", err)
	}
}

func TestLogsFileFlagOverridesConfiguredPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"logging":{"enabled":true,"file":"/does/not/exist/runtime.log"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(home, "override.jsonl")
	if err := os.WriteFile(logPath, []byte(`{"timestamp":"2026-09-30T12:00:00Z","level":"info","event":"override"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out := captureStdout(func() {
		if code := dispatch([]string{"logs", "--file", logPath, "--json"}); code != 0 {
			t.Errorf("logs exit = %d", code)
		}
	})
	if !strings.Contains(out, `"event":"override"`) {
		t.Fatalf("--file override was ignored: %s", out)
	}
}

func TestLogsFlagErrorsDoNotCreateOperationalLog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".odek")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(`{"logging":{"enabled":true}}`), 0600); err != nil {
		t.Fatal(err)
	}
	flush := captureStderr(t)
	if code := dispatch([]string{"logs", "--not-a-real-flag"}); code == 0 {
		t.Fatal("invalid flag accepted")
	}
	_ = flush()
	if _, err := os.Stat(filepath.Join(configDir, "runtime.log")); !os.IsNotExist(err) {
		t.Fatalf("invalid read-only command started writer: %v", err)
	}
}

func TestLogsMissingDefaultIsReadOnlyAndDoesNotCreateConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	out := captureStdout(func() {
		if code := dispatch([]string{"logs", "--json"}); code != 0 {
			t.Errorf("logs exit = %d", code)
		}
	})
	if out != "" {
		t.Fatalf("missing log should produce no JSON records: %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".odek")); !os.IsNotExist(err) {
		t.Fatalf("read-only query created global config directory: %v", err)
	}
}

func TestRetainedLogPathsIncludeGenerationsOldestFirst(t *testing.T) {
	got := retainedLogPaths("x.log", 4)
	want := []string{"x.log.3", "x.log.2", "x.log.1", "x.log"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("paths=%v", got)
	}
}

func TestFollowTracksFileIdentityAcrossRotationAndPrune(t *testing.T) {
	d := t.TempDir()
	active := filepath.Join(d, "runtime.log")
	rotated := active + ".1"
	line1 := `{"timestamp":"2026-09-30T12:00:00Z","event":"first","record_id":"proc-1"}`
	if err := os.WriteFile(active, []byte(line1+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	seen := map[string]fileCursor{}
	recordIDs := logquery.NewIDIndex()
	recordIDs.Add("proc-1")
	if c, ok := statCursor(active); ok {
		seen[c.key()] = c
	}
	if err := os.Rename(active, rotated); err != nil {
		t.Fatal(err)
	}
	recs, _, _, err := readFollowPath(rotated, seen)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("rotation replayed existing line: %+v", recs)
	}
	line2 := `{"timestamp":"2026-09-30T12:01:00Z","event":"second","record_id":"proc-2"}`
	f, err := os.OpenFile(rotated, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(line2 + "\n")
	_ = f.Close()
	recs, _, _, err = readFollowPath(rotated, seen)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := selectFollowRecords(recs, recordIDs, logquery.Filter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Event != "second" {
		t.Fatalf("rotated append lost: %+v", recs)
	}
	// Maintenance pruning replaces the file with a new inode. Start at zero
	// for that new generation so a retained complete record is not lost.
	tmp := rotated + ".new"
	if err := os.WriteFile(tmp, []byte(line2+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, rotated); err != nil {
		t.Fatal(err)
	}
	recs, _, _, err = readFollowPath(rotated, seen)
	if err != nil {
		t.Fatal(err)
	}
	selected, err = selectFollowRecords(recs, recordIDs, logquery.Filter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 0 {
		t.Fatalf("pruned replacement replayed already-seen records: %+v", selected)
	}
	line3 := `{"timestamp":"2026-09-30T12:02:00Z","event":"third","record_id":"proc-3"}`
	f, err = os.OpenFile(rotated, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(line3 + "\n")
	_ = f.Close()
	recs, _, _, err = readFollowPath(rotated, seen)
	if err != nil {
		t.Fatal(err)
	}
	selected, err = selectFollowRecords(recs, recordIDs, logquery.Filter{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Event != "third" {
		t.Fatalf("post-prune new record lost: %+v", selected)
	}
}

func TestFollowDetectsSameInodeTruncation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "runtime.log")
	first := `{"timestamp":"2026-09-30T12:00:00Z","event":"first-long"}`
	if err := os.WriteFile(p, []byte(first+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	seen := map[string]fileCursor{}
	if c, ok := statCursor(p); ok {
		seen[c.key()] = c
	}
	second := `{"timestamp":"2026-09-30T12:01:00Z","event":"next"}`
	if err := os.WriteFile(p, []byte(second+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	recs, _, _, err := readFollowPath(p, seen)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || recs[0].Event != "next" {
		t.Fatalf("truncate was not detected: %+v", recs)
	}
}

func TestFollowEvictsCursorsOutsideRetainedGenerations(t *testing.T) {
	seen := map[string]fileCursor{"old-a": {}, "old-b": {}, "live": {}}
	retainActiveCursors(seen, map[string]bool{"live": true})
	if len(seen) != 1 {
		t.Fatalf("stale cursors retained: %+v", seen)
	}
	if _, ok := seen["live"]; !ok {
		t.Fatal("active generation cursor removed")
	}
}

func TestFollowRecordIDsPreserveEqualEventsAndDeduplicatePrune(t *testing.T) {
	ids := logquery.NewIDIndex()
	filter := logquery.Filter{}
	first := logquery.Record{RecordID: "proc-1", Event: "same", Level: "info"}
	second := logquery.Record{RecordID: "proc-2", Event: "same", Level: "info"}
	got, err := selectFollowRecords([]logquery.Record{first, second}, ids, filter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("equal independent events were collapsed: %+v", got)
	}
	got, err = selectFollowRecords([]logquery.Record{first, second}, ids, filter, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("pruned records replayed: %+v", got)
	}
}

func TestRecordIDRangesHandleOutOfOrderWritesAndSequenceGaps(t *testing.T) {
	ids := logquery.NewIDIndex()
	for _, id := range []string{"process-7", "process-5", "process-6", "process-11", "other-6"} {
		added, err := ids.Add(id)
		if err != nil {
			t.Fatal(err)
		}
		if !added {
			t.Fatalf("first insert rejected: %s", id)
		}
	}
	for _, id := range []string{"process-7", "process-5", "process-6", "process-11", "other-6"} {
		added, err := ids.Add(id)
		if err != nil {
			t.Fatal(err)
		}
		if added {
			t.Fatalf("duplicate not found: %s", id)
		}
	}
	if len(ids.Ranges()) != 3 || ids.Ranges()[1].First != 5 || ids.Ranges()[1].Last != 7 {
		t.Fatalf("sequence ranges did not merge correctly: %+v", ids.Ranges())
	}
}

func TestFollowTreeOnlyIncludesDescendantRuns(t *testing.T) {
	runs := map[string]bool{"root": true, "child": true}
	f := logquery.Filter{RunID: "root", Tree: true}
	selected, err := selectFollowRecords([]logquery.Record{
		{RecordID: "proc-1", RunID: "grandchild", ParentRunID: "child", Event: "included"},
		{RecordID: "proc-2", RunID: "unrelated", ParentRunID: "other", Event: "excluded"},
		{RecordID: "proc-3", RunID: "missing-parent", RootRunID: "root", Event: "recovered"},
	}, logquery.NewIDIndex(), f, runs)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || selected[0].Event != "included" || selected[1].Event != "recovered" {
		t.Fatalf("follow tree membership wrong: %+v", selected)
	}
}

func TestFollowLogsContextPollsAndPrintsOnlyMatchingDescendants(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	if err := os.WriteFile(path, []byte(`{"timestamp":"2026-09-30T12:00:00Z","event":"root","record_id":"proc-1","run_id":"root"}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	filt := logquery.Filter{RunID: "root", Tree: true}
	initial, err := logquery.Read([]string{path}, logquery.Filter{TrackIDs: true}, 100)
	if err != nil {
		t.Fatal(err)
	}
	ids := logquery.NewIDIndex()
	for _, r := range initial.SeenIDRanges {
		if err := ids.AddRange(r); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	out := captureStdout(func() {
		go func() {
			time.Sleep(250 * time.Millisecond)
			f, e := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if e != nil {
				t.Error(e)
				cancel()
				return
			}
			_, e = f.WriteString(`{"timestamp":"2026-09-30T12:00:01Z","event":"child","record_id":"proc-2","run_id":"child","parent_run_id":"root"}` + "\n" + `{"timestamp":"2026-09-30T12:00:02Z","event":"other","record_id":"proc-3","run_id":"other"}` + "\n")
			_ = f.Close()
			if e != nil {
				t.Error(e)
			}
			time.Sleep(250 * time.Millisecond)
			cancel()
		}()
		if err := followLogsContext(ctx, path, 1, filt, false, initial.Positions, ids, map[string]bool{"root": true}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "child") || strings.Contains(out, "other") || strings.Contains(out, "root") {
		t.Fatalf("follow output mismatch: %q", out)
	}
}

func TestRunTreeLateParentAndNoAliveClaim(t *testing.T) {
	records := []logquery.Record{
		{Timestamp: time.Date(2026, 9, 30, 12, 1, 0, 0, time.UTC), RunID: "child", ParentRunID: "root", Status: "done", DurationMS: 25},
		{Timestamp: time.Date(2026, 9, 30, 12, 2, 0, 0, time.UTC), RunID: "root", Status: "done", DurationMS: 50},
	}
	out := captureStdout(func() { renderRunTree(records) })
	if !strings.Contains(out, "root parent= status=done duration=50ms") || !strings.Contains(out, "child parent=root status=done duration=25ms") {
		t.Fatalf("tree summary missing ancestry/status/duration: %s", out)
	}
	if strings.Contains(strings.ToLower(out), "alive") {
		t.Fatalf("tree must not claim a run is still alive: %s", out)
	}
}

func TestRunTreeHandlesParentCyclesWithoutRecursion(t *testing.T) {
	records := []logquery.Record{
		{Timestamp: time.Now(), RunID: "a", ParentRunID: "b", Status: "done"},
		{Timestamp: time.Now(), RunID: "b", ParentRunID: "a", Status: "done"},
	}
	out := captureStdout(func() { renderRunTree(records) })
	if strings.Count(out, "parent=") != 2 {
		t.Fatalf("cycle should render each run once: %q", out)
	}
}

func TestLogsTimeBoundsAndTreeValidation(t *testing.T) {
	if _, err := parseLogTime("bad", true); err == nil {
		t.Fatal("invalid since accepted")
	}
	if _, err := parseLogTime("30m", true); err != nil {
		t.Fatalf("duration since rejected: %v", err)
	}
	if _, err := parseLogTime("2026-09-30T12:00:00Z", false); err != nil {
		t.Fatalf("RFC3339 bound rejected: %v", err)
	}
	if _, err := parseLogTime("30m", false); err == nil {
		t.Fatal("duration accepted for until")
	}
	if err := logsCmd([]string{"--tree"}); err == nil || !strings.Contains(err.Error(), "requires --run") {
		t.Fatalf("tree without run: %v", err)
	}
	if err := logsCmd([]string{"--since", "1h", "--until", "2000-01-01T10:00:00Z", "--file", filepath.Join(t.TempDir(), "empty.log")}); err == nil || !strings.Contains(err.Error(), "after --since") {
		t.Fatalf("inverted bounds accepted: %v", err)
	}
}

func TestHumanRecordRenderingSanitizesTerminalControlCharacters(t *testing.T) {
	r := logquery.Record{Timestamp: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC), Level: "error", Event: "run\n\x1b[31mfailed\u202estatus", Surface: "cli", Status: "bad\nstatus"}
	var out strings.Builder
	renderLogRecord(&out, r)
	if strings.Contains(out.String(), "\x1b") || strings.Contains(out.String(), "\nbad") || strings.Contains(out.String(), "\u202e") {
		t.Fatalf("terminal controls escaped: %q", out.String())
	}
	if !strings.Contains(out.String(), "�") {
		t.Fatalf("control replacement missing: %q", out.String())
	}
}
