package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── In-Memory File Cache ─────────────────────────────────────────────

// fileCache tracks the last-modified time of each known SKILL.md file.
// Used by scanDirCached to skip re-parsing files that haven't changed.
type fileCache map[string]time.Time

// scanDirsCached is the multi-directory equivalent of ScanDirs that uses
// file modification time + content hash caching to skip unchanged files.
// Dirs are scanned in project → user → extras priority order.
func scanDirsCached(projectDir, userDir string, extraDirs []string, fc fileCache, prev skillCache) *ScanResult {
	var dirs []string
	if projectDir != "" {
		dirs = append(dirs, projectDir)
	}
	if userDir != "" {
		dirs = append(dirs, userDir)
	}
	dirs = append(dirs, extraDirs...)

	seen := make(map[string]bool)
	autoLoad := make([]Skill, 0, 10)
	lazy := make([]Skill, 0, 20)

	for _, dir := range dirs {
		skills := scanDirCached(dir, fc, prev)
		for _, s := range skills {
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			if projectDir != "" && dir == projectDir {
				// Mirror ScanDirs: project skills stay distrusted UNLESS
				// the operator promoted this exact content (hash anchored
				// in the trusted user-dir registry, not the attacker-
				// controllable project frontmatter). The cached path used
				// to re-pin unconditionally, making `odek skill promote`
				// a persistent no-op across SkillManager reloads.
				if data, err := os.ReadFile(filepath.Join(dir, s.Name, "SKILL.md")); err != nil || !isPromotedContent(userDir, s.Name, data) {
					markProjectSkill(&s)
				}
			}
			// Provenance gate — see loader.go ScanDirs for rationale.
			if s.AutoLoad && !s.Provenance.NeedsReview {
				autoLoad = append(autoLoad, s)
			} else {
				lazy = append(lazy, s)
			}
		}
	}

	return &ScanResult{AutoLoad: autoLoad, Lazy: lazy}
}

// scanDirCached reads all SKILL.md files in a skill directory, skipping
// files whose mod time AND content hash are unchanged since the last scan.
// The hash anchors the cache on content: a swap that preserves mtime
// (touch -r, Chtimes, rsync -a) invalidates the entry instead of being
// served stale — the injection scan and provenance gate must never be
// silently skipped for changed content. Returns the parsed skills and
// updates the cache.
func scanDirCached(dir string, fc fileCache, prevSkills skillCache) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var skills []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Refuse symlink directory entries — could redirect to arbitrary paths.
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		skillPath := filepath.Join(dir, e.Name(), "SKILL.md")
		info, err := os.Lstat(skillPath)
		if err != nil {
			// File was deleted or inaccessible — remove from cache
			delete(fc, skillPath)
			continue
		}
		// Refuse symlink SKILL.md files.
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}

		currentMod := info.ModTime()
		prevMod, known := fc[skillPath]

		// If mod time is unchanged and the cached parse result's content
		// hash still matches the file, reuse it. Reading + hashing is the
		// cache-integrity check; parsing and the injection scan are what
		// the cache actually skips.
		if known && currentMod.Equal(prevMod) {
			if cached, ok := prevSkills[skillPath]; ok {
				if data, err := os.ReadFile(skillPath); err == nil && contentHashMatches(cached, data) {
					skills = append(skills, cached.Skill)
					continue
				}
			}
		}

		// Parse and cache
		s := parseSkillFile(skillPath)
		if s == nil {
			delete(fc, skillPath)
			continue
		}
		s.Source = SkillSource{Dir: dir, Path: skillPath}
		data, err := os.ReadFile(skillPath)
		if err != nil {
			delete(fc, skillPath)
			continue
		}
		fc[skillPath] = currentMod
		prevSkills[skillPath] = cachedSkill{MTime: currentMod, SHA256: sha256Hex(data), Skill: *s}
		skills = append(skills, *s)
	}
	return skills
}

// contentHashMatches reports whether the cached entry was anchored on the
// given file content. Entries persisted by cache v1 carry no hash and never
// match — they are re-parsed once and re-anchored.
func contentHashMatches(c cachedSkill, data []byte) bool {
	return c.SHA256 != "" && c.SHA256 == sha256Hex(data)
}

// sha256Hex returns the hex-encoded SHA-256 of data.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ── Persistent Disk Cache ─────────────────────────────────────────────

const (
	// cacheVersion is bumped when the cache format changes, automatically
	// invalidating all existing cache files. v2 added the content hash to
	// cachedSkill (mtime-only keys served mtime-preserved content swaps
	// stale, skipping the injection scan and provenance gate).
	cacheVersion = 2

	// cacheFileName is the name of the persistent cache file inside the
	// user's skill directory. The leading dot keeps it hidden from ls.
	cacheFileName = ".skills_cache.json"
)

// persistentCache is the on-disk format for the skill cache.
// Survives across odek process invocations so that stat+parse only
// happens when files actually change.
type persistentCache struct {
	Version int                    `json:"version"`
	Skills  map[string]cachedSkill `json:"skills"` // path → cached skill
}

// cachedSkill pairs a file's mtime AND content hash with its parsed Skill,
// enabling zero-parsing cache hits across process restarts. The hash anchors
// validity on content: an mtime-preserving swap (touch -r, Chtimes, rsync -a)
// invalidates the entry instead of being served stale.
type cachedSkill struct {
	MTime  time.Time `json:"mtime"`
	SHA256 string    `json:"sha256,omitempty"`
	Skill  Skill     `json:"skill"`
}

// cachePath returns the path to the persistent cache file inside dir.
func cachePath(dir string) string {
	return filepath.Join(dir, cacheFileName)
}

// skillCache maps a SKILL.md path to its cached parse result, anchored on
// content hash (see cachedSkill).
type skillCache map[string]cachedSkill

// loadPersistentCache reads the cache file from dir. Returns empty maps
// if the file doesn't exist, has an incompatible version, or is corrupt.
// Never returns an error — degraded behavior is always safe here.
func loadPersistentCache(dir string) (fileCache, skillCache) {
	fileTimes := make(fileCache)
	prevSkills := make(skillCache)

	data, err := os.ReadFile(cachePath(dir))
	if err != nil {
		return fileTimes, prevSkills // file doesn't exist or can't read
	}

	var cache persistentCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return fileTimes, prevSkills // corrupt file
	}

	if cache.Version != cacheVersion {
		return fileTimes, prevSkills // incompatible version
	}

	for path, cs := range cache.Skills {
		fileTimes[path] = cs.MTime
		prevSkills[path] = cs
	}

	return fileTimes, prevSkills
}

// savePersistentCache writes the current fileTimes and prevSkills to disk.
// Entries belonging to skill directories other than the user dir and the
// active project dir are dropped, so the persisted cache never accumulates
// entries for deleted or switched-away projects. Errors are silently
// ignored — the cache is an optimization, not a correctness requirement.
// Atomic write via temp file + rename.
func savePersistentCache(dir, projectDir string, fc fileCache, prev skillCache) {
	if dir == "" {
		return
	}
	cache := persistentCache{
		Version: cacheVersion,
		Skills:  make(map[string]cachedSkill, len(fc)),
	}
	for path, mtime := range fc {
		if projectDir != "" && strings.HasPrefix(path, projectDir+string(filepath.Separator)) {
			// keep current project entries
		} else if strings.HasPrefix(path, dir+string(filepath.Separator)) {
			// keep user-dir entries
		} else {
			continue
		}
		if cs, ok := prev[path]; ok {
			cs.MTime = mtime
			cache.Skills[path] = cs
		}
	}

	data, err := json.Marshal(cache)
	if err != nil {
		return
	}

	target := cachePath(dir)
	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		os.Remove(tmp)
		return
	}
	os.Rename(tmp, target) // best-effort
}

// clearPersistentCache removes the cache file. Called after explicit skill
// mutations (save/patch/delete) to force a full rescan on next Reload.
func clearPersistentCache(dir string) {
	os.Remove(cachePath(dir)) // best-effort
}
