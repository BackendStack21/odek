package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BackendStack21/odek/internal/artifact"
	"github.com/BackendStack21/odek/internal/session"
)

const previewArtifactLimit = 10 << 20
const previewCacheLimit = 40 << 20

type browserArtifact struct {
	ID        string    `json:"id"`
	SessionID string    `json:"session_id"`
	Name      string    `json:"name"`
	MediaType string    `json:"media_type"`
	Size      int       `json:"size_bytes"`
	SHA256    string    `json:"sha256"`
	Created   time.Time `json:"created_at"`
	data      []byte
}
type browserArtifactStore struct {
	sync.Mutex
	entries map[string]browserArtifact
	bytes   int
}

var browserArtifacts = browserArtifactStore{entries: map[string]browserArtifact{}}

// capture takes an immutable, bounded copy through an allowed filesystem root.
// Later downloads cannot race a tool replacing a file or changing a symlink.
func (cache *browserArtifactStore) capture(sid string, ref artifact.Ref, roots []string) (browserArtifact, error) {
	return cache.captureBudget(sid, ref, roots, nil)
}

// previewBudget bounds aggregate content reads across a turn, including failed
// captures, so many references cannot amplify preview work without limit.
type previewBudget struct {
	sync.Mutex
	remaining int64
}

func (b *previewBudget) reserve(n int64) bool {
	if b == nil {
		return true
	}
	b.Lock()
	defer b.Unlock()
	if n > b.remaining {
		return false
	}
	b.remaining -= n
	return true
}

func (cache *browserArtifactStore) captureBudget(sid string, ref artifact.Ref, roots []string, budget *previewBudget) (browserArtifact, error) {
	path, err := artifact.ValidateMetadata(ref, roots)
	if err != nil {
		return browserArtifact{}, err
	}
	var data []byte
	for _, dir := range roots {
		abs, e := filepath.EvalSymlinks(dir)
		if e != nil {
			continue
		}
		abs, e = filepath.Abs(abs)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(abs, path)
		if e != nil || !filepath.IsLocal(rel) {
			continue
		}
		root, e := os.OpenRoot(abs)
		if e != nil {
			continue
		}
		f, e := root.Open(rel)
		if e != nil {
			root.Close()
			continue
		}
		stat, e := f.Stat()
		if e != nil || !stat.Mode().IsRegular() || stat.Size() > previewArtifactLimit {
			f.Close()
			root.Close()
			return browserArtifact{}, fmt.Errorf("artifact exceeds preview limit or is not a regular file")
		}
		if !budget.reserve(stat.Size() + 1) {
			f.Close()
			root.Close()
			return browserArtifact{}, fmt.Errorf("turn preview budget exhausted")
		}
		data, e = io.ReadAll(io.LimitReader(f, stat.Size()+1))
		if e == nil && int64(len(data)) != stat.Size() {
			e = fmt.Errorf("artifact changed during capture")
		}
		f.Close()
		root.Close()
		if e != nil {
			return browserArtifact{}, e
		}
		break
	}
	if data == nil || len(data) > previewArtifactLimit {
		return browserArtifact{}, fmt.Errorf("artifact unavailable")
	}
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	if (ref.SHA256 != "" && ref.SHA256 != sha) || (ref.SizeBytes != nil && *ref.SizeBytes != int64(len(data))) {
		return browserArtifact{}, fmt.Errorf("artifact changed during capture")
	}
	// Content sniffing determines display type; never trust an extension's MIME.
	media := http.DetectContentType(data)
	item := browserArtifact{ID: newTurnID(), SessionID: sid, Name: filepath.Base(path), MediaType: media, Size: len(data), SHA256: sha, Created: time.Now().UTC(), data: data}
	cache.Lock()
	defer cache.Unlock()
	if cache.entries == nil {
		cache.entries = map[string]browserArtifact{}
	}
	for cache.bytes+len(data) > previewCacheLimit || len(cache.entries) >= 128 {
		var oldest string
		var when time.Time
		for id, v := range cache.entries {
			if oldest == "" || v.Created.Before(when) {
				oldest = id
				when = v.Created
			}
		}
		if oldest == "" {
			break
		}
		cache.bytes -= len(cache.entries[oldest].data)
		delete(cache.entries, oldest)
	}
	cache.entries[item.ID] = item
	cache.bytes += len(data)
	return item, nil
}
func handleBrowserArtifacts(store *session.Store, cache *browserArtifactStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return
		}
		sess, code, msg := authenticateJobsRequest(store, r)
		if code != 0 {
			http.Error(w, msg, code)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/artifacts")
		id = strings.TrimPrefix(id, "/")
		cache.Lock()
		if id == "" {
			items := []browserArtifact{}
			for _, item := range cache.entries {
				if item.SessionID == sess.ID {
					items = append(items, item)
				}
			}
			cache.Unlock()
			sort.Slice(items, func(i, j int) bool { return items[i].Created.Before(items[j].Created) })
			writeAPIJSON(w, 200, map[string]any{"artifacts": items, "retention": "Preview cache: up to 40 MiB, cleared on server restart. Save files you want to keep."})
			return
		}
		item, ok := cache.entries[id]
		cache.Unlock()
		if !ok || item.SessionID != sess.ID {
			http.Error(w, "artifact unavailable", 404)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Content-Type", item.MediaType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": item.Name}))
		http.ServeContent(w, r, item.Name, item.Created, bytes.NewReader(item.data))
	}
}

// Uploads are addressed by opaque IDs in prompts. Client-supplied paths never
// become attachment paths; only the server-created, session-bound file is used.
type browserUpload struct {
	restored              bool
	pins                  int
	sessionID, path, name string
	workspace             string
	created               time.Time
	size                  int
}

var browserUploads = struct {
	sync.Mutex
	entries map[string]browserUpload
}{entries: map[string]browserUpload{}}

func handleBrowserUpload(store *session.Store, model, workspace string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return
		}
		var sess *session.Session
		if r.URL.Query().Get("session_id") != "" {
			var code int
			var msg string
			sess, code, msg = authenticateJobsRequest(store, r)
			if code != 0 {
				http.Error(w, msg, code)
				return
			}
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 5<<20))
		if err != nil || len(data) == 0 {
			http.Error(w, "upload must contain 1 byte to 5 MiB", 400)
			return
		}
		name := filepath.Base(r.URL.Query().Get("name"))
		if name == "" || name == "." || len(name) > 200 {
			http.Error(w, "invalid filename", 400)
			return
		}
		// MIME is inspected from bytes. Store only passive media/document types;
		// executable formats and HTML cannot be introduced as media attachments.
		media := strings.Split(http.DetectContentType(data), ";")[0]
		switch media {
		case "image/png", "image/jpeg", "image/gif", "image/webp", "audio/mpeg", "audio/wave", "audio/x-wav", "audio/ogg", "application/ogg", "application/pdf":
		default:
			http.Error(w, "unsupported binary attachment type", 415)
			return
		}
		if sess == nil {
			sess, err = store.Create(nil, model, "Uploaded files")
			if err != nil {
				http.Error(w, "cannot create upload session", 500)
				return
			}
		}
		browserUploads.Lock()
		defer browserUploads.Unlock()
		if _, err := store.Load(sess.ID); err != nil {
			http.Error(w, "upload session unavailable", 409)
			return
		}
		if err := pruneBrowserUploadsLocked(workspace, len(data), time.Now()); err != nil {
			http.Error(w, "upload retention cleanup failed", 503)
			return
		}
		id := newTurnID()
		suffix := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/gif": ".gif", "image/webp": ".webp", "audio/mpeg": ".mp3", "audio/wave": ".wav", "audio/x-wav": ".wav", "audio/ogg": ".ogg", "application/ogg": ".ogg", "application/pdf": ".pdf"}[media]
		rel := filepath.Join(".odek-artifacts", "uploads", sess.ID, id+suffix)
		root, err := openUploadRoot(workspace, true)
		if err != nil {
			http.Error(w, "upload storage unavailable", 500)
			return
		}
		defer root.Close()
		sessionRoot, err := openUploadDir(root, sess.ID, true)
		if err != nil {
			http.Error(w, "upload storage unavailable", 500)
			return
		}
		defer sessionRoot.Close()
		f, err := sessionRoot.OpenFile(filepath.Base(rel), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			http.Error(w, "cannot store upload", 500)
			return
		}
		_, err = f.Write(data)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			_ = sessionRoot.Remove(filepath.Base(rel))
			http.Error(w, "cannot store upload", 500)
			return
		}
		full := filepath.Join(workspace, rel)
		browserUploads.entries[id] = browserUpload{sessionID: sess.ID, path: full, name: name, size: len(data), workspace: workspace, created: time.Now().UTC()}
		digest := sha256.Sum256(data)
		size := int64(len(data))
		ref := artifact.Ref{Schema: artifact.SchemaArtifactRef, ID: id, URI: "file://" + full, MediaType: media, SHA256: hex.EncodeToString(digest[:]), SizeBytes: &size}
		if item, e := browserArtifacts.capture(sess.ID, ref, []string{filepath.Dir(full)}); e == nil {
			browserArtifacts.Lock()
			saved := browserArtifacts.entries[item.ID]
			saved.Name = name
			browserArtifacts.entries[item.ID] = saved
			browserArtifacts.Unlock()
		}
		writeAPIJSON(w, 201, map[string]any{"upload_id": id, "session_id": sess.ID, "auth_token": sess.AuthToken, "name": name, "media_type": media, "size_bytes": len(data)})
	}
}
func resolveBrowserUpload(id, sid string) (browserUpload, bool) {
	browserUploads.Lock()
	defer browserUploads.Unlock()
	item, ok := browserUploads.entries[id]
	return item, ok && !item.restored && item.sessionID == sid
}

// acquireBrowserUpload protects an accepted attachment from retention for a turn.
func acquireBrowserUpload(id, sid string) (browserUpload, func(), bool) {
	browserUploads.Lock()
	defer browserUploads.Unlock()
	item, ok := browserUploads.entries[id]
	if !ok || item.restored || item.sessionID != sid {
		return browserUpload{}, nil, false
	}
	item.pins++
	browserUploads.entries[id] = item
	var once sync.Once
	return item, func() {
		once.Do(func() {
			browserUploads.Lock()
			defer browserUploads.Unlock()
			if current, ok := browserUploads.entries[id]; ok {
				current.pins--
				browserUploads.entries[id] = current
			}
		})
	}, true
}
