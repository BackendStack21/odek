package schedule

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/BackendStack21/odek/internal/fsatomic"
)

// File names under ~/.odek.
const (
	schedulesFile = "schedules.json"      // job definitions
	stateFile     = "schedule-state.json" // runtime state, keyed by job ID
)

// maxScheduleFileBytes caps the size of schedule/state JSON files before they
// are loaded into memory. This prevents a tampered or corrupted multi-gigabyte
// file from OOMing the scheduler or `odek schedule list`.
const maxScheduleFileBytes = 10 << 20 // 10 MiB

// scheduleDoc is the on-disk shape of schedules.json. Wrapping the slice in a
// versioned object leaves room to evolve the format without a breaking change.
type scheduleDoc struct {
	Version int   `json:"version"`
	Jobs    []Job `json:"jobs"`
}

// stateDoc is the on-disk shape of schedule-state.json.
type stateDoc struct {
	Version int                 `json:"version"`
	States  map[string]RunState `json:"states"`
}

// Store persists schedule definitions and runtime state as two JSON files
// under a directory (normally ~/.odek). It is a thin, mutex-guarded file
// manager in the same spirit as session.Store: all Job fields are public, so
// callers read a Job, mutate it, and write it back.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore opens the schedule store rooted at ~/.odek, creating the directory
// if needed.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("schedule: home dir: %w", err)
	}
	return NewStoreAt(filepath.Join(home, ".odek"))
}

// NewStoreAt opens the schedule store rooted at dir. Used by tests and by
// callers that resolve ~/.odek themselves. The directory is created with 0700
// permissions so schedule/state filenames are not listable by other local users.
func NewStoreAt(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("schedule: create dir: %w", err)
	}
	// Best-effort tighten of an existing directory that may have been created
	// with more permissive modes by an earlier version.
	_ = os.Chmod(dir, 0700)
	return &Store{dir: dir}, nil
}

// ── Validation ──────────────────────────────────────────────────────────

// Validate checks that a job is well-formed enough to persist and run: a
// parseable cron expression, a known delivery kind, a non-empty task, and a
// loadable timezone if one is set. It does not assign IDs or defaults.
func (j Job) Validate() error {
	if j.Task == "" {
		return fmt.Errorf("schedule: job %q has an empty task", j.Name)
	}
	loc := time.UTC
	if j.Timezone != "" {
		l, err := time.LoadLocation(j.Timezone)
		if err != nil {
			return fmt.Errorf("schedule: job %q: invalid timezone %q: %w", j.Name, j.Timezone, err)
		}
		loc = l
	}
	sched, err := ParseInLocation(j.Cron, loc)
	if err != nil {
		return fmt.Errorf("schedule: job %q: %w", j.Name, err)
	}
	// Reject expressions that parse but can never match a real date (e.g.
	// "0 0 30 2 *" — Feb 30). Such a job would otherwise have an all-zero
	// next-fire and the engine would treat it as perpetually due.
	if sched.Next(time.Now()).IsZero() {
		return fmt.Errorf("schedule: job %q: cron %q never matches a real date", j.Name, j.Cron)
	}
	switch j.Deliver.Kind {
	case DeliverTelegram, DeliverStdout, DeliverLog:
	case "":
		return fmt.Errorf("schedule: job %q has no delivery kind", j.Name)
	default:
		return fmt.Errorf("schedule: job %q has unknown delivery kind %q", j.Name, j.Deliver.Kind)
	}
	return nil
}

// ── Job CRUD ────────────────────────────────────────────────────────────

// Add validates and appends a job. If job.ID is empty a stable ID is
// generated; if job.CreatedAt is zero it is stamped with now. The stored job
// (with ID/CreatedAt filled in) is returned.
func (s *Store) Add(job Job) (Job, error) {
	if err := job.Validate(); err != nil {
		return Job{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.fileLock()
	if err != nil {
		return Job{}, err
	}
	defer release()

	doc, err := s.loadDoc()
	if err != nil {
		return Job{}, err
	}
	if job.ID == "" {
		job.ID = newJobID()
	}
	for _, existing := range doc.Jobs {
		if existing.ID == job.ID {
			return Job{}, fmt.Errorf("schedule: job ID %q already exists", job.ID)
		}
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	doc.Jobs = append(doc.Jobs, job)
	if err := s.saveDoc(doc); err != nil {
		return Job{}, err
	}
	return job, nil
}

// List returns all jobs, sorted by creation time then ID for stable output.
func (s *Store) List() ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadDoc()
	if err != nil {
		return nil, err
	}
	sort.Slice(doc.Jobs, func(i, j int) bool {
		if doc.Jobs[i].CreatedAt.Equal(doc.Jobs[j].CreatedAt) {
			return doc.Jobs[i].ID < doc.Jobs[j].ID
		}
		return doc.Jobs[i].CreatedAt.Before(doc.Jobs[j].CreatedAt)
	})
	return doc.Jobs, nil
}

// Get returns the job with the given ID. The bool reports whether it was found.
func (s *Store) Get(id string) (Job, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.loadDoc()
	if err != nil {
		return Job{}, false, err
	}
	for _, j := range doc.Jobs {
		if j.ID == id {
			return j, true, nil
		}
	}
	return Job{}, false, nil
}

// Put upserts a job by ID: it replaces an existing job with the same ID, or
// appends it if absent. The job is validated first.
func (s *Store) Put(job Job) error {
	if err := job.Validate(); err != nil {
		return err
	}
	if job.ID == "" {
		return fmt.Errorf("schedule: Put requires a job ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.fileLock()
	if err != nil {
		return err
	}
	defer release()
	doc, err := s.loadDoc()
	if err != nil {
		return err
	}
	for i := range doc.Jobs {
		if doc.Jobs[i].ID == job.ID {
			if job.CreatedAt.IsZero() {
				job.CreatedAt = doc.Jobs[i].CreatedAt
			}
			doc.Jobs[i] = job
			return s.saveDoc(doc)
		}
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now().UTC()
	}
	doc.Jobs = append(doc.Jobs, job)
	return s.saveDoc(doc)
}

// Remove deletes a job (and its runtime state) by ID. Removing a job that
// does not exist returns an error so the CLI can report it.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.fileLock()
	if err != nil {
		return err
	}
	defer release()
	doc, err := s.loadDoc()
	if err != nil {
		return err
	}
	idx := -1
	for i := range doc.Jobs {
		if doc.Jobs[i].ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("schedule: no job with ID %q", id)
	}
	doc.Jobs = append(doc.Jobs[:idx], doc.Jobs[idx+1:]...)
	if err := s.saveDoc(doc); err != nil {
		return err
	}
	// Best-effort cleanup of orphaned runtime state.
	sd, err := s.loadState()
	if err == nil {
		if _, ok := sd.States[id]; ok {
			delete(sd.States, id)
			_ = s.saveState(sd)
		}
	}
	return nil
}

// SetEnabled flips a job's Enabled flag.
func (s *Store) SetEnabled(id string, enabled bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.fileLock()
	if err != nil {
		return err
	}
	defer release()
	doc, err := s.loadDoc()
	if err != nil {
		return err
	}
	for i := range doc.Jobs {
		if doc.Jobs[i].ID == id {
			doc.Jobs[i].Enabled = enabled
			return s.saveDoc(doc)
		}
	}
	return fmt.Errorf("schedule: no job with ID %q", id)
}

// ModTime returns the last-modified time of the schedules file, or the zero
// time if it does not exist yet. The engine polls this for cheap hot-reload
// detection without parsing the file.
func (s *Store) ModTime() time.Time {
	info, err := os.Stat(filepath.Join(s.dir, schedulesFile))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// ── Runtime state ───────────────────────────────────────────────────────

// LoadState returns runtime state for all jobs, keyed by job ID. A missing
// state file yields an empty (non-nil) map.
func (s *Store) LoadState() (map[string]RunState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sd, err := s.loadState()
	if err != nil {
		return nil, err
	}
	return sd.States, nil
}

// SaveState writes (or replaces) the runtime state for a single job. Other
// jobs' state is preserved.
func (s *Store) SaveState(st RunState) error {
	if st.JobID == "" {
		return fmt.Errorf("schedule: SaveState requires a JobID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	release, err := s.fileLock()
	if err != nil {
		return err
	}
	defer release()
	sd, err := s.loadState()
	if err != nil {
		return err
	}
	sd.States[st.JobID] = st
	return s.saveState(sd)
}

// ── Internal IO (callers hold s.mu) ─────────────────────────────────────

func (s *Store) loadDoc() (*scheduleDoc, error) {
	doc := &scheduleDoc{Version: 1}
	if err := readJSON(filepath.Join(s.dir, schedulesFile), doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func (s *Store) saveDoc(doc *scheduleDoc) error {
	if doc.Version == 0 {
		doc.Version = 1
	}
	return writeJSONAtomic(filepath.Join(s.dir, schedulesFile), doc)
}

func (s *Store) loadState() (*stateDoc, error) {
	sd := &stateDoc{Version: 1, States: map[string]RunState{}}
	if err := readJSON(filepath.Join(s.dir, stateFile), sd); err != nil {
		return nil, err
	}
	if sd.States == nil {
		sd.States = map[string]RunState{}
	}
	return sd, nil
}

func (s *Store) saveState(sd *stateDoc) error {
	if sd.Version == 0 {
		sd.Version = 1
	}
	return writeJSONAtomic(filepath.Join(s.dir, stateFile), sd)
}

// readJSON decodes path into v. A missing file is not an error — v is left at
// its zero/default value so callers start from an empty document. Files larger
// than maxScheduleFileBytes are rejected to prevent OOM from a tampered or
// corrupted multi-gigabyte blob.
func readJSON(path string, v any) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("schedule: stat %s: %w", filepath.Base(path), err)
	}
	if info.Size() > maxScheduleFileBytes {
		return fmt.Errorf("schedule: %s is too large (%d bytes, max %d)", filepath.Base(path), info.Size(), maxScheduleFileBytes)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("schedule: read %s: %w", filepath.Base(path), err)
	}
	if len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("schedule: parse %s: %w", filepath.Base(path), err)
	}
	return nil
}

// writeJSONAtomic marshals v and writes it to path via a temp file + rename,
// so a reader never observes a half-written file and a swapped-in symlink is
// replaced rather than followed. Files are 0600 since tasks may reference
// secrets.
//
// The actual atomic write is delegated to internal/fsatomic, which uses a
// random temp name with O_EXCL (so a pre-created symlink cannot be opened)
// and fsyncs both the data and the parent directory before returning.
func writeJSONAtomic(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("schedule: marshal %s: %w", filepath.Base(path), err)
	}
	if err := fsatomic.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("schedule: write %s: %w", filepath.Base(path), err)
	}
	return nil
}

// fileLock takes an exclusive OS lock (flock) on ~/.odek/schedules.lock and
// returns a release func. The in-process mutex only serialises one process;
// this serialises the read-modify-write cycle ACROSS processes, so two
// concurrent `odek schedule add` invocations can't both load the same baseline
// and clobber each other's write. Lock acquisition is now a hard error: if the
// lock file can't be opened or locked, the caller aborts the mutating operation
// rather than proceeding without cross-process serialization. Callers hold
// s.mu, so there is a single lock order (mu → flock) and no deadlock with the
// read-only methods, which take only s.mu.
func (s *Store) fileLock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(s.dir, "schedules.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("schedule: cannot open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("schedule: cannot acquire lock: %w", err)
	}
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// newJobID returns a stable, collision-resistant job ID like "jb-1a2b3c4d".
func newJobID() string {
	buf := make([]byte, 4)
	rand.Read(buf) //nolint:errcheck // crypto/rand.Read never returns an error on supported platforms
	return "jb-" + hex.EncodeToString(buf)
}
