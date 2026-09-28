package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/BackendStack21/odek/internal/diagnostics"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/maintenance"
	"github.com/BackendStack21/odek/internal/session"
)

func operationalTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ODEK_LOGGING_ENABLED", "true")
	t.Chdir(home)
	if err := os.Mkdir(filepath.Join(home, ".odek"), 0700); err != nil {
		t.Fatal(err)
	}
	return home
}

func readOperationalRecords(t *testing.T, home string) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(home, ".odek", "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "PRIVATE") {
		t.Fatalf("private data leaked: %s", b)
	}
	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatal(err)
		}
		if record["process_id"] == "" || record["pid"] != float64(os.Getpid()) {
			t.Fatal(record)
		}
		records = append(records, record)
	}
	return records
}

func TestOperationalLoggingStartupAndCommandFailure(t *testing.T) {
	home := operationalTestHome(t)
	if err := os.WriteFile(filepath.Join(home, ".odek", "config.json"), []byte(`{"PRIVATE":`), 0600); err != nil {
		t.Fatal(err)
	}
	if code := dispatch([]string{"run", "--PRIVATE-invalid-flag"}); code == 0 {
		t.Fatal("invalid command succeeded")
	}
	records := readOperationalRecords(t, home)
	var configFailure, commandFailure bool
	for _, record := range records {
		data := record["data"].(map[string]any)
		configFailure = configFailure || data["operation"] == "decode_file"
		commandFailure = commandFailure || data["component"] == "cli" && record["level"] == "ERROR"
	}
	if !configFailure || !commandFailure {
		t.Fatalf("missing startup/command diagnostics: %+v", records)
	}
}

func TestOperationalLoggingStorageAndMaintenance(t *testing.T) {
	home := operationalTestHome(t)
	closeLog := startOperationalLogging("test")
	t.Cleanup(closeLog)
	store, err := session.NewStoreWithDir(filepath.Join(home, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(nil, "test", "PRIVATE task")
	if err != nil {
		t.Fatal(err)
	}
	// Replacing index.json with a directory forces a real persistence failure.
	index := filepath.Join(home, "sessions", "index.json")
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(index, 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveNoIndex(sess); err == nil {
		t.Fatal("save unexpectedly succeeded")
	}
	brokenHome := filepath.Join(home, "broken")
	if err := os.MkdirAll(filepath.Join(brokenHome, "runtime.log"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := maintenance.Sweep(t.Context(), brokenHome, maintenance.Config{RuntimeLogMaxAgeHours: 1, LogMaxMB: 1}); err == nil {
		t.Fatal("sweep unexpectedly succeeded")
	}
	closeLog()
	records := readOperationalRecords(t, home)
	operations := map[string]bool{}
	for _, record := range records {
		data := record["data"].(map[string]any)
		operations[data["component"].(string)+"/"+data["operation"].(string)] = true
		if data["component"] == "session" && record["session_id"] != sess.ID {
			t.Fatal("session correlation lost")
		}
	}
	for _, op := range []string{"session/save", "maintenance/runtime_log_retention", "maintenance/log_rotation"} {
		if !operations[op] {
			t.Fatalf("missing %s: %+v", op, records)
		}
	}
}

func TestOperationalLoggingDisabledAndUnavailable(t *testing.T) {
	home := operationalTestHome(t)
	t.Setenv("ODEK_LOGGING_ENABLED", "false")
	closeLog := startOperationalLogging("test")
	diagnostics.Report("test", "disabled", "", syscall.ENOSPC)
	closeLog()
	path := filepath.Join(home, ".odek", "runtime.log")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("disabled logger created file")
	}
	t.Setenv("ODEK_LOGGING_ENABLED", "true")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	closeLog = startOperationalLogging("test")
	diagnostics.Report("test", "unavailable", "", syscall.ENOSPC)
	closeLog() // logging failure must not crash the command or recurse
}

func TestOperationalLoggingKeepsCleanupPreviewReadOnly(t *testing.T) {
	home := operationalTestHome(t)
	if code := dispatch([]string{"cleanup", "--dry-run"}); code != 0 {
		t.Fatalf("preview exit %d", code)
	}
	for _, name := range []string{"runtime.log", "runtime.log.lock"} {
		if _, err := os.Stat(filepath.Join(home, ".odek", name)); !os.IsNotExist(err) {
			t.Fatalf("preview created %s", name)
		}
	}
}

func TestHTTPDiagnosticsPrivacyAndResponseSemantics(t *testing.T) {
	var got []events.Event
	restore := diagnostics.Install(func(ev events.Event) { got = append(got, ev) })
	defer restore()
	mux := http.NewServeMux()
	mux.HandleFunc("/failed/{id}", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "PRIVATE response", 503) })
	mux.HandleFunc("/flush", func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush(); _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/panic", func(w http.ResponseWriter, r *http.Request) { panic("PRIVATE panic") })
	handler := diagnosticHTTPHandler(mux)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/failed/PRIVATE?token=PRIVATE", nil))
	if w.Code != 503 || !strings.Contains(w.Body.String(), "PRIVATE response") {
		t.Fatal("response changed")
	}
	if len(got) != 1 || got[0].Data["operation"] != "/failed/{id}" || got[0].Data["http_status"] != 503 || got[0].Type != "operation_failed" {
		t.Fatal(got)
	}
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/PRIVATE-missing", nil))
	if got[1].Type != "operation_warning" || got[1].Data["operation"] != "unmatched_route" {
		t.Fatal(got[1])
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/flush", nil))
	if !w.Flushed || w.Body.String() != "ok" || len(got) != 2 {
		t.Fatal("flush behavior changed")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/ok", nil))
	if w.Code != http.StatusOK || w.Body.String() != "ok" || len(got) != 2 {
		t.Fatal("implicit successful response changed")
	}
	func() {
		defer func() {
			if recover() != "PRIVATE panic" {
				t.Error("panic swallowed or changed")
			}
		}()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/panic", nil))
	}()
	if len(got) != 3 || got[2].Type != "panic_recovered" {
		t.Fatal(got)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "PRIVATE") {
		t.Fatalf("request or panic leaked: %s", b)
	}
	recorder := &diagnosticResponseWriter{ResponseWriter: httptest.NewRecorder()}
	if _, _, err := recorder.Hijack(); !errors.Is(err, http.ErrNotSupported) {
		t.Fatal(err)
	}
	if recorder.Unwrap() != recorder.ResponseWriter {
		t.Fatal("response controller cannot unwrap")
	}
}
