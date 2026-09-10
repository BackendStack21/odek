package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BackendStack21/odek/internal/artifact"
	"github.com/BackendStack21/odek/internal/session"
)

func TestWorkspaceSchedulesCRUD(t *testing.T) {
	h := handleSchedules(t.TempDir())
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		w := httptest.NewRecorder()
		h(w, r)
		return w
	}
	body := `{"name":"Daily review","cron":"0 9 * * 1-5","task":"Review changes","deliver":{"kind":"log"},"enabled":false,"timezone":"Europe/Berlin"}`
	w := call("POST", "/api/schedules", body)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var job map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &job)
	id := job["id"].(string)
	job["enabled"] = true
	b, _ := json.Marshal(job)
	if w = call("POST", "/api/schedules/"+id, string(b)); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if w = call("GET", "/api/schedules", ""); !strings.Contains(w.Body.String(), `"enabled":true`) {
		t.Fatal(w.Body.String())
	}
	if w = call("DELETE", "/api/schedules/"+id, ""); w.Code != 204 {
		t.Fatal(w.Body.String())
	}
	if w = call("POST", "/api/schedules", `{"cron":"invalid"}`); w.Code != 400 {
		t.Fatal(w.Code)
	}
}

func TestWorkspaceArtifactCaptureIsImmutableAndScoped(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "result.txt")
	if err := os.WriteFile(file, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	cache := &browserArtifactStore{}
	ref := artifact.Ref{Schema: artifact.SchemaArtifactRef, ID: "a", URI: (&url.URL{Scheme: "file", Path: file}).String(), MediaType: "text/plain"}
	store, err := session.NewStoreWithDir(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	a, err := store.Create(nil, "fixture", "A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Create(nil, "fixture", "B")
	if err != nil {
		t.Fatal(err)
	}
	item, err := cache.capture(a.ID, ref, []string{dir})
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(file, []byte("changed"), 0600)
	h := handleBrowserArtifacts(store, cache)
	req := httptest.NewRequest("GET", "/api/artifacts/"+item.ID+"?session_id="+a.ID, nil)
	req.Header.Set("X-Session-Token", a.AuthToken)
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != 200 || w.Body.String() != "original" {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/artifacts/"+item.ID+"?session_id="+b.ID, nil)
	req.Header.Set("X-Session-Token", b.AuthToken)
	w = httptest.NewRecorder()
	h(w, req)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	if _, err := cache.capture(a.ID, ref, nil); err == nil {
		t.Fatal("accepted artifact without allowed roots")
	}
	ref.SHA256 = strings.Repeat("0", 64)
	if _, err := cache.capture(a.ID, ref, []string{dir}); err == nil {
		t.Fatal("accepted wrong digest")
	}
}

func TestWorkspaceUploadRejectsExecutableAndBindsSession(t *testing.T) {
	store, err := session.NewStoreWithDir(filepath.Join(t.TempDir(), "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	h := handleBrowserUpload(store, "fixture", workspace)
	req := httptest.NewRequest("POST", "/api/uploads?name=script.html", strings.NewReader("<html><script>alert(1)</script></html>"))
	w := httptest.NewRecorder()
	h(w, req)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Fatal(w.Code, w.Body.String())
	}
	// PNG signature is sufficient for MIME detection; bytes are never executed.
	png := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 0}
	req = httptest.NewRequest("POST", "/api/uploads?name=image.png", bytes.NewReader(png))
	w = httptest.NewRecorder()
	h(w, req)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	id := result["upload_id"].(string)
	sid := result["session_id"].(string)
	upload, ok := resolveBrowserUpload(id, sid)
	if !ok {
		t.Fatal("missing upload")
	}
	rel, err := filepath.Rel(workspace, upload.path)
	if err != nil || !filepath.IsLocal(rel) || filepath.Ext(upload.path) != ".png" {
		t.Fatalf("upload is not accessible inside workspace: %s", upload.path)
	}
	if data, err := os.ReadFile(upload.path); err != nil || !bytes.Equal(data, png) {
		t.Fatalf("upload bytes unavailable: %v", err)
	}
	if _, ok := resolveBrowserUpload(id, "different"); ok {
		t.Fatal("cross-session upload accepted")
	}
	if strings.Contains(w.Body.String(), store.Dir()) {
		t.Fatal("leaked storage path")
	}
}
