package maintenance

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/runtimelog"
)

func TestSweepRuntimeLogExpiration(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "runtime.log")
	expired, _ := json.Marshal(events.Event{Type: "run_completed", Timestamp: time.Now().Add(-48 * time.Hour), SessionID: "expired"})
	recent, _ := json.Marshal(events.Event{Type: "run_completed", Timestamp: time.Now(), SessionID: "recent"})
	raw := append(append(append(expired, '\n'), recent...), '\n')
	for _, name := range []string{path, path + ".1"} {
		if err := os.WriteFile(name, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	policy := runtimelog.Options{Path: path, MaxFiles: 2, MaxAgeHours: 0}
	rep, err := Sweep(context.Background(), home, Config{RuntimeLog: policy})
	if err != nil || rep.RuntimeLogRecordsRemoved != 0 {
		t.Fatalf("zero retention: %+v %v", rep, err)
	}
	policy.MaxAgeHours = 24
	rep, err = Sweep(context.Background(), home, Config{RuntimeLog: policy})
	if err != nil || rep.RuntimeLogRecordsRemoved != 2 {
		t.Fatalf("sweep: %+v %v", rep, err)
	}
	for _, name := range []string{path, path + ".1"} {
		b, _ := os.ReadFile(name)
		if strings.Contains(string(b), "expired") || !strings.Contains(string(b), "recent") {
			t.Fatalf("retention: %s", b)
		}
	}
}

func TestSweepUsesConfiguredCustomPathAcrossGenerations(t *testing.T) {
	home := t.TempDir()
	customDir := filepath.Join(t.TempDir(), "nested")
	if err := os.MkdirAll(customDir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(customDir, "operator.jsonl")
	expired, _ := json.Marshal(runtimelog.Record{Schema: runtimelog.Schema, Timestamp: time.Now().Add(-48 * time.Hour), Level: "info", Event: "run.started", RecordID: "expired"})
	recent, _ := json.Marshal(runtimelog.Record{Schema: runtimelog.Schema, Timestamp: time.Now(), Level: "info", Event: "run.started", RecordID: "recent"})
	raw := append(append(append(expired, '\n'), recent...), '\n')
	for _, name := range []string{path, path + ".1", path + ".2"} {
		if err := os.WriteFile(name, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	defaultPath := filepath.Join(home, "runtime.log")
	if err := os.WriteFile(defaultPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	policy := runtimelog.Options{Path: path, MaxFiles: 3, MaxAgeHours: 24}
	rep, err := Sweep(context.Background(), home, Config{RuntimeLog: policy})
	if err != nil || rep.RuntimeLogRecordsRemoved != 3 {
		t.Fatalf("custom path sweep: %+v, %v", rep, err)
	}
	for _, name := range []string{path, path + ".1", path + ".2"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "expired") || !strings.Contains(string(b), "recent") {
			t.Fatalf("retention %s: %s", name, b)
		}
	}
	untouched, err := os.ReadFile(defaultPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(untouched), "expired") {
		t.Fatal("custom retention swept the default path too")
	}
}

func TestSweepRotatesOnlyConfiguredRuntimeLogGenerations(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(t.TempDir(), "custom.log")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", (1<<20)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".1", []byte("previous"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".3", []byte("stale generation"), 0600); err != nil {
		t.Fatal(err)
	}
	policy := runtimelog.Options{Path: path, MaxFileMB: 1, MaxFiles: 3}
	rep, err := Sweep(context.Background(), home, Config{RuntimeLog: policy})
	if err != nil || len(rep.LogsRotated) != 1 || rep.LogsRotated[0] != path {
		t.Fatalf("rotation report: %+v, %v", rep, err)
	}
	if info, err := os.Stat(path); err != nil || info.Size() != 0 {
		t.Fatalf("active file: %v %v", info, err)
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil || len(backup) != (1<<20)+1 {
		t.Fatalf("active generation was not retained: size=%d err=%v", len(backup), err)
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatalf("excess generation remains: %v", err)
	}
}

func TestSweepEnforcesMaxFilesWhenAgeAndSizeLimitsAreDisabled(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(t.TempDir(), "runtime.log")
	for _, name := range []string{path, path + ".1", path + ".2", path + ".9"} {
		if err := os.WriteFile(name, []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	policy := runtimelog.Options{Path: path, MaxFiles: 2, MaxFileMB: 0, MaxAgeHours: 0}
	if _, err := Sweep(context.Background(), home, Config{RuntimeLog: policy}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + ".1"} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("configured generation missing %s: %v", name, err)
		}
	}
	for _, name := range []string{path + ".2", path + ".9"} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("excess generation remains %s: %v", name, err)
		}
	}
}
