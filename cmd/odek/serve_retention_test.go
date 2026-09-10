package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BackendStack21/odek/internal/artifact"
	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/session"
)

func TestServeDeletedSessionStopsJobsAndUploads(t *testing.T) {
	workspace := t.TempDir()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(nil, "model", "test")
	if err != nil {
		t.Fatal(err)
	}
	mgr := bgproc.NewManager(bgproc.Config{}, nil)
	defer mgr.Shutdown()
	wireServeSessionCleanup(store, mgr, workspace)
	turnCtx, cancelTurn := context.WithCancel(context.Background())
	defer cancelTurn()
	unregister := registerPromptCancel(sess.ID, cancelTurn)
	defer unregister()
	png := []byte{137, 80, 78, 71, 13, 10, 26, 10, 0, 0, 0, 0}
	req := httptest.NewRequest("POST", "/api/uploads?name=image.png&session_id="+sess.ID, bytes.NewReader(png))
	req.Header.Set("X-Session-Token", sess.AuthToken)
	out := httptest.NewRecorder()
	handleBrowserUpload(store, "model", workspace)(out, req)
	if out.Code != 201 {
		t.Fatal(out.Code, out.Body.String())
	}
	var upload struct {
		ID string `json:"upload_id"`
	}
	_ = json.Unmarshal(out.Body.Bytes(), &upload)
	item, ok := resolveBrowserUpload(upload.ID, sess.ID)
	if !ok {
		t.Fatal("upload unavailable")
	}
	job, err := mgr.Start(sess.ID, "sleep 30", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	if turnCtx.Err() == nil {
		t.Fatal("deleted session turn not cancelled")
	}
	sess.Messages = append(sess.Messages, session.Message{Role: "assistant", Content: "Turn aborted"})
	if err := store.SaveNoIndex(sess); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled turn recreated deleted session: %v", err)
	}
	if _, ok = resolveBrowserUpload(upload.ID, sess.ID); ok {
		t.Fatal("deleted session upload still resolves")
	}
	if _, err = os.Stat(item.path); !os.IsNotExist(err) {
		t.Fatal("deleted upload still on disk")
	}
	got, _ := mgr.Get(sess.ID, job.ID)
	if got.Status == bgproc.StatusRunning {
		t.Fatal("deleted session job running")
	}
	if _, err = mgr.Start(sess.ID, "true", "", 0); err == nil {
		t.Fatal("stale tool launched after deletion")
	}
}

func TestUploadSweepBoundsRestartStorageAndRejectsSymlinks(t *testing.T) {
	workspace := t.TempDir()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(nil, "model", "test")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(workspace, ".odek-artifacts", "uploads", sess.ID)
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 130; i++ {
		if err = os.WriteFile(filepath.Join(dir, fmt.Sprintf("restored-%03d.png", i)), []byte("image"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := filepath.Join(dir, "expired.png")
	_ = os.WriteFile(old, []byte("old"), 0600)
	past := time.Now().Add(-8 * 24 * time.Hour)
	_ = os.Chtimes(old, past, past)
	if err = sweepBrowserUploads(store, workspace, time.Now()); err != nil {
		t.Fatal(err)
	}
	files, _ := os.ReadDir(dir)
	if len(files) != uploadHandleLimit {
		t.Fatalf("retained %d", len(files))
	}
	if _, err = os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expired file retained")
	}
	if _, ok := resolveBrowserUpload("restored-129", sess.ID); ok {
		t.Fatal("disk file became authenticated upload")
	}
	// A directory substitution must never point retention at another subtree.
	outside := t.TempDir()
	marker := filepath.Join(outside, "keep")
	_ = os.WriteFile(marker, []byte("safe"), 0600)
	hostile := t.TempDir()
	_ = os.Symlink(outside, filepath.Join(hostile, ".odek-artifacts"))
	if err = sweepBrowserUploads(store, hostile, time.Now()); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("retention escaped its root")
	}
	_ = deleteBrowserSession(workspace, sess.ID)
}

func TestArtifactCaptureBudgetAndDigest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.txt")
	_ = os.WriteFile(path, []byte("four"), 0600)
	ref := artifact.Ref{Schema: artifact.SchemaArtifactRef, ID: "a", URI: (&url.URL{Scheme: "file", Path: path}).String(), MediaType: "text/plain"}
	cache := &browserArtifactStore{}
	budget := &previewBudget{remaining: 5}
	if _, err := cache.captureBudget("s", ref, []string{dir}, budget); err != nil {
		t.Fatal(err)
	}
	if _, err := cache.captureBudget("s", ref, []string{dir}, budget); err == nil {
		t.Fatal("aggregate budget ignored")
	}
	ref.SHA256 = strings.Repeat("0", 64)
	if _, err := cache.captureBudget("s", ref, []string{dir}, &previewBudget{remaining: 10}); err == nil {
		t.Fatal("digest verification skipped")
	}
}

func TestMemoryApplyDoesNotRequireProvider(t *testing.T) {
	cfg := config.ResolvedConfig{}
	cfg.Provider = "missing-provider-for-local-apply"
	cfg.Model = "missing-model"
	req := httptest.NewRequest("POST", "/api/memory/consolidate", strings.NewReader(`{"target":"user","mode":"apply","preview":{"before":[],"after":["Prefers concise answers"]}}`))
	w := httptest.NewRecorder()
	handleMemoryConsolidate(t.TempDir(), cfg)(w, req)
	if w.Code != 204 {
		t.Fatalf("local apply: %d %s", w.Code, w.Body.String())
	}
}

func TestUploadSweepEnforcesPersistentByteCap(t *testing.T) {
	workspace := t.TempDir()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(nil, "model", "test")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(workspace, ".odek-artifacts", "uploads", sess.ID)
	_ = os.MkdirAll(dir, 0700)
	for i := 0; i < 54; i++ {
		f, err := os.Create(filepath.Join(dir, fmt.Sprintf("large-%d.png", i)))
		if err != nil {
			t.Fatal(err)
		}
		err = f.Truncate(5 << 20)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = sweepBrowserUploads(store, workspace, time.Now()); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var size int64
	for _, f := range files {
		info, err := f.Info()
		if err != nil {
			t.Fatal(err)
		}
		size += info.Size()
	}
	if size > uploadDiskLimit {
		t.Fatalf("disk cap exceeded: %d", size)
	}
	_ = deleteBrowserSession(workspace, sess.ID)
}

func TestRetentionStopsJobsDeletedByAnotherStore(t *testing.T) {
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(nil, "model", "test")
	if err != nil {
		t.Fatal(err)
	}
	mgr := bgproc.NewManager(bgproc.Config{}, nil)
	defer mgr.Shutdown()
	job, err := mgr.Start(sess.ID, "sleep 30", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	other, err := session.NewStoreWithDir(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if err = other.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sweepDeletedJobSessions(ctx, store, mgr)
	got, _ := mgr.Get(sess.ID, job.ID)
	if got.Status == bgproc.StatusRunning {
		t.Fatal("janitor-deleted session still running")
	}
	if _, err = mgr.Start(sess.ID, "true", "", 0); err == nil {
		t.Fatal("late launch after janitor deletion")
	}
}

func TestRetentionPinsActiveAttachmentAndRejectsOverflow(t *testing.T) {
	workspace := t.TempDir()
	store, err := session.NewStoreWithDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Create(nil, "model", "test")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(workspace, ".odek-artifacts", "uploads", sess.ID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "pinned.pdf")
	if err := os.WriteFile(path, []byte("attachment"), 0600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * uploadMaxAge)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	browserUploads.Lock()
	browserUploads.entries["pinned"] = browserUpload{sessionID: sess.ID, path: path, workspace: workspace, created: old, size: 10}
	browserUploads.Unlock()
	defer func() { browserUploads.Lock(); delete(browserUploads.entries, "pinned"); browserUploads.Unlock() }()
	_, release, ok := acquireBrowserUpload("pinned", sess.ID)
	if !ok {
		t.Fatal("acquire failed")
	}
	defer release()
	if err := sweepBrowserUploads(store, workspace, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("active attachment removed: %v", err)
	}
	browserUploads.Lock()
	err = pruneBrowserUploadsLocked(workspace, uploadDiskLimit, time.Now())
	browserUploads.Unlock()
	if err == nil {
		t.Fatal("capacity exceeded with pinned upload")
	}
	release()
	if err := sweepBrowserUploads(store, workspace, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("released expired attachment retained")
	}
}
