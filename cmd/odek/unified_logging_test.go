package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/BackendStack21/odek"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/runtimelog"
)

func TestUnifiedServeRunUsesRESTIdentityAndSingleTerminal(t *testing.T) {
	for _, failed := range []bool{false, true} {
		name := "completed"
		if failed {
			name = "failed"
		}
		t.Run(name, func(t *testing.T) {
			provider := mockLLM(t, func(w http.ResponseWriter, _ int) {
				w.Header().Set("Content-Type", "application/json")
				if failed {
					w.WriteHeader(401)
					_, _ = w.Write([]byte(`{"error":{"message":"PRIVATE provider body"}}`))
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"PRIVATE answer"}}]}`))
			})
			defer provider.Close()
			path := filepath.Join(t.TempDir(), "runtime.log")
			env := newRestRunEnv(t, provider.URL, func(c *config.ResolvedConfig) {
				c.Logging = config.LoggingConfig{Enabled: true, File: path, Level: "debug", MaxFiles: 4, MaxFileMB: 25, MaxAgeHours: 168}
			})
			_, resp := startTestRun(t, env, `{"content":"PRIVATE prompt"}`)
			id := resp["run_id"].(string)
			snap := waitRunStatus(t, id, 20*time.Second)
			if snap["status"] != name {
				t.Fatalf("status=%v", snap)
			}
			if !drainServeWork(10 * time.Second) {
				t.Fatal("run did not drain")
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "PRIVATE") {
				t.Fatal("content leaked into metadata log")
			}
			starts, ends := 0, 0
			for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
				var r runtimelog.Record
				if err := json.Unmarshal([]byte(line), &r); err != nil {
					t.Fatal(err)
				}
				if r.Event == "run.started" {
					starts++
					if r.RunID != id {
						t.Fatal("REST/log run ID mismatch")
					}
				}
				if r.Event == "run.finished" {
					ends++
					if r.RunID != id || r.Status != name || r.SessionID != snap["session_id"] || r.TurnID == "" {
						t.Fatalf("terminal correlation: %+v", r)
					}
				}
			}
			if starts != 1 || ends != 1 {
				t.Fatalf("starts=%d terminals=%d", starts, ends)
			}
			if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".odek", "serve.log")); !os.IsNotExist(err) {
				t.Fatal("legacy serve.log was created")
			}
		})
	}
}

func TestSurfaceLoggerDropsFreeTextAndClassifiesErrors(t *testing.T) {
	var got []events.Event
	restore := diagnostics.Install(func(e events.Event) { got = append(got, e) })
	defer restore()
	l := newOperationalSurfaceLogger("telegram").With("PRIVATE key", "PRIVATE value")
	l.Debug("PRIVATE message", "text", "PRIVATE body")
	l.Info("PRIVATE message")
	l.Warn("PRIVATE message", "error", syscall.ENOSPC)
	l.Error("PRIVATE message", "err", errors.New("PRIVATE error"))
	if len(got) != 4 || got[2].Data["error_class"] != "disk_full" {
		t.Fatal(got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "PRIVATE") {
		t.Fatalf("privacy regression: %s", b)
	}
	for _, e := range got {
		if e.Data["operation"] != "diagnostic" {
			t.Fatal(e)
		}
	}
}

func TestOperationalLoggingCustomPathPrunesAtStartup(t *testing.T) {
	home := operationalTestHome(t)
	path := filepath.Join(home, "custom", "events.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	old, _ := json.Marshal(runtimelog.Record{Schema: runtimelog.Schema, Timestamp: time.Now().Add(-48 * time.Hour), Event: "run.finished", Level: "info"})
	if err := os.WriteFile(path, append(old, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODEK_LOGGING_FILE", path)
	t.Setenv("ODEK_LOGGING_MAX_AGE_HOURS", "24")
	closeLog := startOperationalLogging("test")
	diagnostics.Report("test", "disk", "", syscall.ENOSPC)
	closeLog()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "run.finished") || !strings.Contains(string(body), "operation.failed") {
		t.Fatalf("startup retention/output: %s", body)
	}
	if _, err := os.Stat(filepath.Join(home, ".odek", "runtime.log")); !os.IsNotExist(err) {
		t.Fatal("default destination used despite custom path")
	}
}

func TestOperationalAndAgentShareWriterAcrossSurfaces(t *testing.T) {
	home := operationalTestHome(t)
	path := filepath.Join(home, "runtime.log")
	t.Setenv("ODEK_LOGGING_FILE", path)
	closeLog := startOperationalLogging("telegram")
	defer closeLog()
	resolved := config.LoadConfig(config.CLIFlags{})
	cfg := odek.Config{APIKey: "test", Model: "test", NoProjectFile: true, SystemMessage: "Test", RuntimeLogSurface: "schedule"}
	applyResolvedProvider(&cfg, resolved)
	cfg.APIKey = "test"
	a, err := odek.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.BeginRun("shared-run", "shared-turn")
	a.FinishRun(syscall.ENOSPC)
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	diagnostics.Emit(events.Event{Type: "service_started", Data: map[string]any{"component": "schedule"}})
	diagnostics.Report("telegram", "disk", "", syscall.ENOSPC)
	closeLog()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(body)), "\n") {
		var r runtimelog.Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatal(err)
		}
		if r.Event == "run.finished" && r.RunID == "shared-run" && r.Surface == "schedule" {
			seen["run"] = true
		}
		if r.Event == "service.started" && r.Surface == "schedule" {
			seen["embedded"] = true
		}
		if r.Event == "operation.failed" && r.Surface == "telegram" {
			seen["operational"] = true
		}
	}
	if len(seen) != 3 {
		t.Fatalf("shared writer lost records: %s", body)
	}
}

func TestSurfaceInvocationFinalizerRecordsPanicAndFailure(t *testing.T) {
	for _, panics := range []bool{false, true} {
		t.Run(map[bool]string{false: "error", true: "panic"}[panics], func(t *testing.T) {
			var records []events.Event
			a, err := odek.New(odek.Config{APIKey: "test", Model: "test", SystemMessage: "Test", NoProjectFile: true, EventHandler: func(ev events.Event) { records = append(records, ev) }})
			if err != nil {
				t.Fatal(err)
			}
			a.BeginRun("surface", "turn")
			func() {
				defer func() {
					if value := recover(); (panics && value != "private panic") || (!panics && value != nil) {
						t.Errorf("panic not preserved: %v", value)
					}
				}()
				outcome := error(syscall.ENOSPC)
				defer finishAgentInvocation(a, &outcome)
				if panics {
					panic("private panic")
				}
			}()
			_ = a.Close()
			ends := 0
			for _, ev := range records {
				if ev.Type == events.TypeRunFailed {
					ends++
					if !panics && ev.Data["error_class"] != "disk_full" {
						t.Fatal(ev)
					}
				}
				if ev.Type == events.TypeRunCompleted {
					t.Fatal("false success")
				}
			}
			if ends != 1 {
				t.Fatalf("terminal count=%d", ends)
			}
		})
	}
}
