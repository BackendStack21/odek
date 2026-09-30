package runtimelog

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/events"
)

func TestV1RecordTranslationIsFlatAndMetadataOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	l, err := OpenWithOptions(Options{Path: path, Surface: "cli", Level: "debug", MaxFiles: 4})
	if err != nil {
		t.Fatal(err)
	}
	l.Emit(events.Event{Type: "run_started", SessionID: "s", TurnID: "t", RunID: "r", ParentRunID: "p", RootRunID: "root", TaskID: "task", SourceTaskID: "source", ParentTaskID: "pt", ParentTurnID: "parent-turn", Data: map[string]any{"call_id": "call", "args": "SECRET PROMPT", "model": "gpt-5", "duration_ms": int64(12)}})
	l.Emit(events.Event{Type: "run_failed", Data: map[string]any{"error_class": "provider_auth", "http_status": 401, "attempts": 2, "message": "SECRET ERROR"}})
	l.Emit(events.Event{Type: "run_failed", Data: map[string]any{"error_class": "context_canceled", "message": "SECRET CANCEL"}})
	l.Close()
	r := records(t, path)
	if len(r) != 3 {
		t.Fatalf("records=%d want=3", len(r))
	}
	start := r[0]
	if start.Schema != Schema || start.Event != "run.started" || start.Level != "info" || start.Surface != "cli" || start.CallID != "call" || start.DurationMS != 12 || start.SourceTaskID != "source" || start.ParentTaskID != "pt" || start.ParentTurnID != "parent-turn" {
		t.Fatalf("start record missing v1 fields: %+v", start)
	}
	if _, ok := start.Metadata["args"]; ok {
		t.Fatal("prompt field crossed metadata boundary")
	}
	failed := r[1]
	if failed.Event != "run.finished" || failed.Status != "failed" || failed.Level != "error" || failed.Error == nil || failed.Error.Code != "provider.auth" || failed.Error.HTTPStatus != 401 || failed.Error.Attempts != 2 {
		t.Fatalf("failed terminal record: %+v", failed)
	}
	cancelled := r[2]
	if cancelled.Status != "cancelled" || cancelled.Level != "info" || cancelled.Error != nil {
		t.Fatalf("cancelled record: %+v", cancelled)
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), `"type"`) {
		t.Fatalf("legacy/content field leaked: %s", b)
	}
}

func TestAcquireSharesWriterAcrossSurfacesAndChecksPolicy(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	opts := Options{Path: path, Surface: "cli", Level: "debug", MaxFileMB: 1, MaxFiles: 4, MaxAgeHours: 168}
	a, releaseA, err := Acquire(opts)
	if err != nil {
		t.Fatal(err)
	}
	opts.Surface = "schedule"
	b, releaseB, err := Acquire(opts)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("same process/path opened duplicate writers")
	}
	if _, _, err := Acquire(Options{Path: path, Surface: "telegram", Level: "info", MaxFileMB: 1, MaxFiles: 4, MaxAgeHours: 168}); err == nil {
		t.Fatal("accepted incompatible logger policy")
	}
	a.EmitForSurface(events.Event{Type: "service_started"}, "cli")
	b.EmitForSurface(events.Event{Type: "schedule_delivered"}, "schedule")
	releaseA()
	b.EmitForSurface(events.Event{Type: "service_stopped"}, "telegram")
	releaseB()
	r := records(t, path)
	if len(r) != 3 || r[0].Surface != "cli" || r[1].Surface != "schedule" || r[2].Surface != "telegram" {
		t.Fatalf("surface records: %+v", r)
	}
}

func TestConfiguredRotationAndStartupRetention(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	for i, value := range []string{"one", "two", "three", "four"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := RotateWithOptions(path, 1, 4); err != nil {
			t.Fatal(err)
		}
		if i < 3 {
			if err := os.WriteFile(path, []byte("active"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, want := range []string{"four", "three", "two"} {
		b, err := os.ReadFile(path + "." + string(rune('1'+i)))
		if err != nil || string(b) != want {
			t.Fatalf("backup %d = %q, %v; want %q", i+1, b, err, want)
		}
	}
	old := time.Now().Add(-10 * time.Hour)
	line, _ := json.Marshal(Record{Schema: Schema, Timestamp: old, Level: "info", Event: "run.started"})
	if err := os.WriteFile(path+".3", append(line, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	n, err := PruneAtStartup(context.Background(), Options{Path: path, MaxFiles: 4, MaxAgeHours: 1})
	if err != nil || n != 1 {
		t.Fatalf("startup prune=%d,%v; want 1,nil", n, err)
	}
	contents, err := os.ReadFile(path + ".3")
	if err != nil || len(contents) != 0 {
		t.Fatalf("expired backup contents=%q err=%v", contents, err)
	}
}

func TestConcurrentAcquireUsesOneWriter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	const workers = 16
	var wg sync.WaitGroup
	loggers := make(chan *Logger, workers)
	releases := make(chan func(), workers)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			l, release, err := Acquire(Options{Path: path, Surface: "worker", Level: "debug", MaxFiles: 4})
			if err != nil {
				t.Errorf("Acquire: %v", err)
				return
			}
			loggers <- l
			releases <- release
		}(i)
	}
	close(start)
	wg.Wait()
	close(loggers)
	close(releases)
	var shared *Logger
	for l := range loggers {
		if shared == nil {
			shared = l
		} else if shared != l {
			t.Fatal("concurrent acquire made duplicate writers")
		}
	}
	for release := range releases {
		release()
	}
	if len(records(t, path)) != 0 {
		t.Fatal("empty acquisitions unexpectedly emitted records")
	}
}

func TestStartupPruneNoParentAndCancelledPrune(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing", "runtime.log")
	if n, err := PruneAtStartup(context.Background(), Options{Path: missing, MaxFiles: 4, MaxAgeHours: 168}); err != nil || n != 0 {
		t.Fatalf("missing parent: %d, %v", n, err)
	}
	path := filepath.Join(t.TempDir(), "runtime.log")
	old, _ := json.Marshal(Record{Schema: Schema, Timestamp: time.Now().Add(-time.Hour), Level: "info", Event: "run.started"})
	raw := append(old, '\n')
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := PruneWithOptions(ctx, path, time.Now(), false, 4); err == nil {
		t.Fatal("cancelled retention succeeded")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("cancelled prune changed source: %q, %v", got, err)
	}
}

func TestLowerFileCountRemovesExcessNumberedBackupsAndRejectsSymlink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.log")
	if err := os.WriteFile(path, []byte("active"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".8", []byte("stale"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateWithOptions(path, 0, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".8"); !os.IsNotExist(err) {
		t.Fatalf("stale generation remains: %v", err)
	}
	if err := os.WriteFile(path, []byte("active again"), 0600); err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, path+".9"); err != nil {
		t.Fatal(err)
	}
	if _, err := RotateWithOptions(path, 0, 2); err == nil {
		t.Fatal("accepted symlink excess generation")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "active again" {
		t.Fatalf("symlink rejection damaged active log: %q %v", got, err)
	}
	got, err = os.ReadFile(victim)
	if err != nil || string(got) != "keep" {
		t.Fatalf("symlink target changed: %q %v", got, err)
	}
}

func TestDefaultOptionsPathExpansionAndMetadataValidation(t *testing.T) {
	if got := DefaultOptions(); got.Path != "~/.odek/runtime.log" || got.Level != "info" || got.MaxFileMB != 25 || got.MaxFiles != 4 || got.MaxAgeHours != 168 {
		t.Fatalf("defaults: %+v", got)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := expandHome("~"); err != nil || got != home {
		t.Fatalf("expand ~ = %q, %v", got, err)
	}
	if got, err := expandHome("~/logs/runtime.log"); err != nil || got != filepath.Join(home, "logs/runtime.log") {
		t.Fatalf("expand home path = %q, %v", got, err)
	}
	if safeMetadataString("/failed/{id}") == false || safeMetadataString("/Users/person/private.txt") || safeMetadataString("bad value") || safeMetadataString("../private") {
		t.Fatal("metadata route/path filtering is unsafe")
	}
	if got := canonicalErrorCode("rate_limited"); got != "provider.rate_limited" {
		t.Fatalf("canonical error code=%q", got)
	}
	if got := canonicalErrorCode("custom_issue"); got != "custom.issue" {
		t.Fatalf("generic error code=%q", got)
	}
	for _, tc := range []struct {
		in   any
		want int
	}{{int(2), 2}, {int32(3), 3}, {int64(4), 4}, {float32(5), 5}, {float64(6), 6}, {"bad", 0}} {
		if got := scalarInt(tc.in); got != tc.want {
			t.Fatalf("scalarInt(%v)=%d want=%d", tc.in, got, tc.want)
		}
	}
	path := filepath.Join(t.TempDir(), "runtime.log")
	l, err := OpenWithOptions(Options{Path: path, Level: "nonsense", MaxFiles: 100})
	if err != nil {
		t.Fatal(err)
	}
	if l.level != "info" || l.maxFiles != 32 {
		t.Fatalf("logger option bounds: level=%q files=%d", l.level, l.maxFiles)
	}
	l.Close()
}
