package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/bgproc"
	"github.com/BackendStack21/odek/internal/session"
)

const uploadDiskLimit = 256 << 20
const uploadHandleLimit = 128
const uploadMaxAge = 7 * 24 * time.Hour

// openUploadDir pins each directory and rejects symlinks, including a swap
// between inspection and opening. Retention never walks an arbitrary subtree.
func openUploadDir(parent *os.Root, name string, create bool) (*os.Root, error) {
	if create {
		if err := parent.Mkdir(name, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, err
		}
	}
	info, err := parent.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("upload directory is not a regular directory")
	}
	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	opened, err := fs.Stat(child.FS(), ".")
	if err != nil || !os.SameFile(info, opened) {
		child.Close()
		return nil, fmt.Errorf("upload directory changed")
	}
	return child, nil
}
func openUploadRoot(workspace string, create bool) (*os.Root, error) {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	artifacts, err := openUploadDir(root, ".odek-artifacts", create)
	if err != nil {
		return nil, err
	}
	defer artifacts.Close()
	return openUploadDir(artifacts, "uploads", create)
}

func removeBrowserUploadLocked(id string, item browserUpload) error {
	root, err := openUploadRoot(item.workspace, false)
	if errors.Is(err, fs.ErrNotExist) {
		delete(browserUploads.entries, id)
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	dir, err := openUploadDir(root, item.sessionID, false)
	if errors.Is(err, fs.ErrNotExist) {
		delete(browserUploads.entries, id)
		return nil
	}
	if err != nil {
		return err
	}
	err = dir.Remove(filepath.Base(item.path))
	dir.Close()
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	delete(browserUploads.entries, id)
	_ = root.Remove(item.sessionID) // remove an empty session directory only
	return nil
}

// The caller holds browserUploads so eviction cannot race a new handle.
func pruneBrowserUploadsLocked(workspace string, incoming int, now time.Time) error {
	type entry struct {
		id   string
		item browserUpload
	}
	var entries []entry
	var bytes int
	for id, item := range browserUploads.entries {
		if item.workspace == workspace {
			entries = append(entries, entry{id, item})
			bytes += item.size
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].item.created.Before(entries[j].item.created) })
	count := len(entries)
	for _, e := range entries {
		if e.item.pins > 0 {
			continue
		}
		if now.Sub(e.item.created) <= uploadMaxAge && bytes+incoming <= uploadDiskLimit && count+boolInt(incoming > 0) <= uploadHandleLimit {
			continue
		}
		if err := removeBrowserUploadLocked(e.id, e.item); err != nil {
			return err
		}
		bytes -= e.item.size
		count--
	}
	if incoming > 0 && (bytes+incoming > uploadDiskLimit || count+1 > uploadHandleLimit) {
		return fmt.Errorf("upload capacity is held by active turns")
	}
	return nil
}
func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// Recover disk accounting after a restart, never authenticated upload handles.
// Workspace files are mutable, so only uploads received by this process resolve.
// Only the server's two-level session/file layout is read; symlinks are rejected.
func sweepBrowserUploads(store *session.Store, workspace string, now time.Time) error {
	browserUploads.Lock()
	defer browserUploads.Unlock()
	root, err := openUploadRoot(workspace, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()

	sessions, err := root.Open(".")
	if err != nil {
		return err
	}
	defer sessions.Close()
	for {
		dirs, readErr := sessions.ReadDir(64)
		for _, d := range dirs {
			sid := d.Name()
			if !d.IsDir() || session.ValidateSessionID(sid) != nil {
				continue
			}
			dir, err := openUploadDir(root, sid, false)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			files, err := dir.Open(".")
			if err != nil {
				dir.Close()
				return err
			}
			_, sessionErr := os.Stat(store.Path(sid))
			if sessionErr != nil && !errors.Is(sessionErr, fs.ErrNotExist) {
				files.Close()
				dir.Close()
				return sessionErr
			}
			err = scanUploadFilesLocked(files, dir, workspace, sid, sessionErr != nil, now)
			files.Close()
			dir.Close()
			if err != nil {
				return err
			}
			_ = root.Remove(sid) // only empty directories
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}

	// Expired files removed during the walk may still have in-memory handles.
	for id, item := range browserUploads.entries {
		if item.workspace == workspace {
			if _, err := root.Stat(filepath.Join(item.sessionID, filepath.Base(item.path))); errors.Is(err, fs.ErrNotExist) {
				delete(browserUploads.entries, id)
			}
		}
	}
	return pruneBrowserUploadsLocked(workspace, 0, now)
}

// ReadDir batches bound memory even when stale files accumulated before limits.
func scanUploadFilesLocked(files *os.File, dir *os.Root, workspace, sid string, deleted bool, now time.Time) error {
	for {
		entries, readErr := files.ReadDir(64)
		for _, entry := range entries {
			info, err := entry.Info()
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				continue
			}
			full := filepath.Join(workspace, ".odek-artifacts", "uploads", sid, entry.Name())
			id := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			if item, ok := browserUploads.entries[id]; ok && item.path == full && item.pins > 0 && !deleted {
				continue
			}
			if deleted || info.Size() > 5<<20 || now.Sub(info.ModTime()) > uploadMaxAge {
				if err = dir.Remove(entry.Name()); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return err
				}
				continue
			}
			if item, ok := browserUploads.entries[id]; ok && item.path == full {
				item.size = int(info.Size())
				browserUploads.entries[id] = item
			} else {
				key := "disk:" + sid + "/" + entry.Name()
				browserUploads.entries[key] = browserUpload{sessionID: sid, path: full, name: entry.Name(), size: int(info.Size()), workspace: workspace, created: info.ModTime(), restored: true}
			}
			if err = pruneBrowserUploadsLocked(workspace, 0, now); err != nil {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func deleteBrowserSession(workspace, sid string) error {
	if err := session.ValidateSessionID(sid); err != nil {
		return err
	}
	browserUploads.Lock()
	defer browserUploads.Unlock()
	root, err := openUploadRoot(workspace, false)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if root != nil {
		defer root.Close()
		if err = root.RemoveAll(sid); err != nil {
			return err
		}
	}
	for id, item := range browserUploads.entries {
		if item.workspace == workspace && item.sessionID == sid {
			delete(browserUploads.entries, id)
		}
	}
	browserArtifacts.Lock()
	defer browserArtifacts.Unlock()
	for id, item := range browserArtifacts.entries {
		if item.SessionID == sid {
			browserArtifacts.bytes -= len(item.data)
			delete(browserArtifacts.entries, id)
		}
	}
	return nil
}

func wireServeSessionCleanup(store *session.Store, mgr *bgproc.Manager, workspace string) {
	previous := store.OnDelete
	store.OnDelete = func(sid string) {
		cancelPrompt(sid)
		if mgr != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := mgr.DeleteSession(ctx, sid)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "odek: session job cleanup: %v\n", err)
			}
		}
		if err := deleteBrowserSession(workspace, sid); err != nil {
			fmt.Fprintf(os.Stderr, "odek: session upload cleanup: %v\n", err)
		}
		if previous != nil {
			previous(sid)
		}
	}
}

func sweepDeletedJobSessions(ctx context.Context, store *session.Store, mgr *bgproc.Manager) {
	if mgr == nil {
		return
	}
	for _, sid := range mgr.ActiveSessions() {
		if _, err := os.Stat(store.Path(sid)); errors.Is(err, fs.ErrNotExist) {
			cancelPrompt(sid)
			if err := mgr.DeleteSession(ctx, sid); err != nil {
				fmt.Fprintf(os.Stderr, "odek: deleted session job cleanup: %v\n", err)
			}
		}
	}
}

func startServeRetention(ctx context.Context, store *session.Store, workspace string, mgr *bgproc.Manager) {
	sweep := func() {
		drainCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		sweepDeletedJobSessions(drainCtx, store, mgr)
		cancel()
		if err := sweepBrowserUploads(store, workspace, time.Now()); err != nil {
			fmt.Fprintf(os.Stderr, "odek: upload retention: %v\n", err)
		}
		retrySandboxCleanup()
	}
	sweep()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				sweep()
			}
		}
	}()
}
