package logquery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeJSONL(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestReadFiltersLimitAndMalformed(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "runtime.log")
	writeJSONL(t, p,
		`{"timestamp":"2026-09-30T12:00:00Z","level":"info","event":"start","surface":"cli"}`,
		`not json`,
		`{"timestamp":"2026-09-30T12:01:00Z","level":"WARN","event":"retry","surface":"serve","status":"retrying","error":{"code":"busy"}}`,
		`{"timestamp":"2026-09-30T12:02:00Z","level":"error","event":"failed","surface":"serve","status":"failed","error":{"code":"busy"}}`)
	res, err := Read([]string{p}, Filter{Level: "warn", Surface: "serve", ErrorCode: "busy", Since: time.Date(2026, 9, 30, 12, 0, 30, 0, time.UTC)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if res.Malformed != 1 || len(res.Records) != 1 || res.Records[0].Event != "failed" {
		t.Fatalf("unexpected result: %+v", res)
	}
}

func TestIDIndexBoundsAndOutOfOrderIntervals(t *testing.T) {
	idx := NewIDIndex()
	for _, id := range []string{"process-7", "process-5", "process-6", "process-11"} {
		added, err := idx.Add(id)
		if err != nil || !added {
			t.Fatalf("add %q: added=%t err=%v", id, added, err)
		}
	}
	if added, err := idx.Add("process-6"); err != nil || added {
		t.Fatalf("duplicate: added=%t err=%v", added, err)
	}
	got := idx.Ranges()
	if len(got) != 2 || got[0].First != 5 || got[0].Last != 7 || got[1].First != 11 {
		t.Fatalf("unexpected compact intervals: %+v", got)
	}
	for _, bad := range []string{"", "bad", strings.Repeat("a", MaxRecordIDBytes+1) + "-1", "bad/slash-1"} {
		if _, err := idx.Add(bad); err == nil {
			t.Fatalf("accepted malformed ID %q", bad)
		}
	}
	if err := idx.AddRange(IDRange{ProcessID: strings.Repeat("x", MaxProcessIDBytes+1), First: 1, Last: 2}); err == nil {
		t.Fatal("accepted oversized process range")
	}
}

func TestIDIndexAddRangeMergesAndBoundsProcesses(t *testing.T) {
	idx := NewIDIndex()
	for _, r := range []IDRange{{ProcessID: "p", First: 5, Last: 7}, {ProcessID: "p", First: 1, Last: 4}, {ProcessID: "p", First: 9, Last: 10}, {ProcessID: "p", First: 8, Last: 8}} {
		if err := idx.AddRange(r); err != nil {
			t.Fatal(err)
		}
	}
	got := idx.Ranges()
	if len(got) != 1 || got[0].First != 1 || got[0].Last != 10 {
		t.Fatalf("ranges did not merge: %+v", got)
	}
	for i := 0; i < MaxIDProcesses-1; i++ {
		if err := idx.AddRange(IDRange{ProcessID: fmt.Sprintf("proc%d", i), First: 1, Last: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.AddRange(IDRange{ProcessID: "overflow", First: 1, Last: 1}); err == nil {
		t.Fatal("process limit was not enforced")
	}
}

func TestReadTrackIDsIndependentOfFiltersAndLimit(t *testing.T) {
	p := filepath.Join(t.TempDir(), "runtime.log")
	writeJSONL(t, p,
		`{"timestamp":"2026-09-30T12:00:00+02:00","level":"debug","event":"filtered","record_id":"process-1"}`,
		`{"timestamp":"2026-09-30T12:00:01Z","level":"error","event":"kept","record_id":"process-2"}`)
	res, err := Read([]string{p}, Filter{Level: "error", TrackIDs: true}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 || res.Records[0].Event != "kept" {
		t.Fatalf("filter mismatch: %+v", res.Records)
	}
	if len(res.SeenIDRanges) != 1 || res.SeenIDRanges[0].First != 1 || res.SeenIDRanges[0].Last != 2 {
		t.Fatalf("filtered IDs were not seeded: %+v", res.SeenIDRanges)
	}
	if _, offset := res.Records[0].Timestamp.Zone(); offset != 0 || res.Records[0].Timestamp.Location() != time.UTC {
		t.Fatalf("timestamp not UTC: %v", res.Records[0].Timestamp)
	}
}

func TestReadFollowRejectsLegacyAndMalformedIDs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "runtime.log")
	for _, line := range []string{
		`{"timestamp":"2026-09-30T12:00:00Z","event":"legacy"}`,
		`{"timestamp":"2026-09-30T12:00:00Z","event":"bad","record_id":"` + strings.Repeat("x", MaxRecordIDBytes+1) + `-1"}`,
	} {
		writeJSONL(t, p, line)
		if _, err := Read([]string{p}, Filter{TrackIDs: true}, 100); err == nil {
			t.Fatalf("accepted unsafe follow fixture %q", line[:min(100, len(line))])
		}
	}
}

func TestReadRotationsPreservesEqualRecordsAndMissingIsReadOnly(t *testing.T) {
	d := t.TempDir()
	missing := filepath.Join(d, "missing.log")
	if _, err := Read([]string{missing + ".3", missing}, Filter{}, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("read created log: %v", err)
	}
	p := filepath.Join(d, "runtime.log")
	line := `{"timestamp":"2026-09-30T12:00:00Z","level":"info","event":"same"}`
	writeJSONL(t, p+".1", line)
	writeJSONL(t, p, line)
	res, err := Read([]string{p + ".1", p}, Filter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 2 {
		t.Fatalf("equal records must remain distinct, got %d", len(res.Records))
	}
}

func TestReadRejectsSymlink(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "target")
	link := filepath.Join(d, "runtime.log")
	writeJSONL(t, target, `{"timestamp":"2026-09-30T12:00:00Z","event":"x"}`)
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Read([]string{link}, Filter{}, 10); err == nil {
		t.Fatal("symlink accepted")
	}
}

func TestReadOversizedRecordIsSkippedAndContinues(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "runtime.log")
	good := `{"timestamp":"2026-09-30T12:00:00Z","level":"info","event":"kept"}`
	if err := os.WriteFile(p, []byte(strings.Repeat("x", MaxLineBytes+1)+"\n"+good+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	res, err := Read([]string{p}, Filter{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Malformed != 1 || len(res.Records) != 1 || res.Records[0].Event != "kept" {
		t.Fatalf("unexpected oversized handling: %+v", res)
	}
}

func TestTreeIncludesRecursiveDescendantsWithoutParentRecord(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "runtime.log")
	writeJSONL(t, p,
		`{"timestamp":"2026-09-30T12:00:00Z","event":"child","run_id":"child","parent_run_id":"root","status":"ok","duration_ms":5}`,
		`{"timestamp":"2026-09-30T12:01:00Z","event":"grandchild","run_id":"grandchild","parent_run_id":"child","status":"failed","duration_ms":9}`,
		`{"timestamp":"2026-09-30T12:02:00Z","event":"other","run_id":"other","parent_run_id":"elsewhere"}`)
	res, err := Read([]string{p}, Filter{RunID: "root", Tree: true, Status: "failed"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 || res.Records[0].RunID != "grandchild" {
		t.Fatalf("descendant filter failed: %+v", res.Records)
	}
}

func TestRunFilterIsExactAndTreeUsesRootIdentityAfterParentPruning(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "runtime.log")
	writeJSONL(t, p,
		`{"timestamp":"2026-09-30T12:00:00Z","level":"info","event":"child","run_id":"child","root_run_id":"root"}`,
		`{"timestamp":"2026-09-30T12:01:00Z","level":"info","event":"root","run_id":"root"}`)
	exact, err := Read([]string{p}, Filter{RunID: "root"}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(exact.Records) != 1 || exact.Records[0].RunID != "root" {
		t.Fatalf("plain run filter included descendants: %+v", exact.Records)
	}
	tree, err := Read([]string{p}, Filter{RunID: "root", Tree: true}, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Records) != 2 {
		t.Fatalf("tree missed root_run_id descendant with missing parent: %+v", tree.Records)
	}
}

func TestCopyAvailableHoldsPartialLineThenReadsOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "follow.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	partial := `{"timestamp":"2026-09-30T12:00:00Z","event":"partial"}`
	if _, err := f.WriteString(partial[:len(partial)-1]); err != nil {
		t.Fatal(err)
	}
	recs, offset, bad, err := CopyAvailable(f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 || offset != 0 || bad != 0 {
		t.Fatalf("partial line consumed: %v %d %d", recs, offset, bad)
	}
	if _, err := f.WriteString(partial[len(partial)-1:] + "\n"); err != nil {
		t.Fatal(err)
	}
	recs, offset, bad, err = CopyAvailable(f, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 || offset != int64(len(partial)+1) || bad != 0 {
		t.Fatalf("completed line not read once: %v %d %d", recs, offset, bad)
	}
	recs, _, _, err = CopyAvailable(f, offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 0 {
		t.Fatalf("duplicate read: %v", recs)
	}
}

func TestReadAppliesFiltersBeforeBoundedRecordBuffer(t *testing.T) {
	p := filepath.Join(t.TempDir(), "runtime.log")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fmt.Fprintln(f, `{"timestamp":"2026-09-30T12:00:00Z","level":"error","event":"wanted","error":{"code":"needle"}}`)
	line := `{"timestamp":"2026-09-30T12:00:01Z","level":"debug","event":"noise"}` + "\n"
	for i := 0; i < MaxRecords+1; i++ {
		if _, err := f.WriteString(line); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	res, err := Read([]string{p}, Filter{ErrorCode: "needle"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Records) != 1 || res.Records[0].Event != "wanted" {
		t.Fatalf("early matching record was lost: %+v", res.Records)
	}
	if res.Truncated {
		t.Fatal("irrelevant records should not consume the bounded match buffer")
	}
}

func TestReadEnforcesSharedByteBudgetAcrossRetainedFiles(t *testing.T) {
	d := t.TempDir()
	old := filepath.Join(d, "runtime.log.1")
	active := filepath.Join(d, "runtime.log")
	payload := strings.Repeat("x", 900<<10)
	var lines []string
	for i := 0; i < 10; i++ {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-09-30T12:00:%02dZ","level":"info","event":"old-%02d","metadata":{"model":"%s"}}`, i, i, payload))
	}
	writeJSONL(t, old, lines...)
	lines = lines[:0]
	for i := 10; i < 20; i++ {
		lines = append(lines, fmt.Sprintf(`{"timestamp":"2026-09-30T12:00:%02dZ","level":"info","event":"new-%02d","metadata":{"model":"%s"}}`, i, i, payload))
	}
	writeJSONL(t, active, lines...)
	res, err := Read([]string{old, active}, Filter{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Truncated {
		t.Fatal("expected query buffer cap notice")
	}
	if len(res.Records) == 0 || len(res.Records) > MaxRecords {
		t.Fatalf("record count is not bounded: %d", len(res.Records))
	}
	if res.Records[len(res.Records)-1].Event != "new-19" {
		t.Fatalf("newest matching records were not retained: %q", res.Records[len(res.Records)-1].Event)
	}
}
