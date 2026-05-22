package skills

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// ── In-Memory File Cache ─────────────────────────────────────────────

// fileCache tracks the last-modified time of each known SKILL.md file.
// Used by scanDirCached to skip re-parsing files that haven't changed.
type fileCache map[string]time.Time

// scanDirsCached is the multi-directory equivalent of ScanDirs that uses
// file modification time caching to skip unchanged files. Dirs are scanned
// in project → user → extras priority order.
func scanDirsCached(projectDir, userDir string, extraDirs []string, fc fileCache, prev map[string]Skill) *ScanResult {
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
			if s.AutoLoad {
				autoLoad = append(autoLoad, s)
			} else {
				lazy = append(lazy, s)
			}
		}
	}

	return &ScanResult{AutoLoad: autoLoad, Lazy: lazy}
}

// scanDirCached reads all SKILL.md files in a skill directory, skipping
// files whose mod time has not changed since the last scan. Returns the
// parsed skills and updates the cache with current mod times.
func scanDirCached(dir string, fc fileCache, prevSkills map[string]Skill) []Skill {
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

		// If mod time is unchanged and we have a cached parse result, reuse it
		if known && currentMod.Equal(prevMod) {
			if cached, ok := prevSkills[skillPath]; ok {
				skills = append(skills, cached)
				continue
			}
		}

		// Parse and cache
		s := parseSkillFile(skillPath)
		if s == nil {
			delete(fc, skillPath)
			continue
		}
		s.Source = SkillSource{Dir: dir, Path: skillPath}
		fc[skillPath] = currentMod
		prevSkills[skillPath] = *s
		skills = append(skills, *s)
	}
	return skills
}

// ── Persistent Disk Cache ─────────────────────────────────────────────

const (
	// cacheVersion is bumped when the cache format changes, automatically
	// invalidating all existing cache files.
	cacheVersion = 1

	// cacheFileName is the name of the persistent cache file inside the
	// user's skill directory. The leading dot keeps it hidden from ls.
	cacheFileName = ".skills_cache.json"
)

// persistentCache is the on-disk format for the skill cache.
// Survives across odek process invocations so that stat+parse only
// happens when files actually change.
type persistentCache struct {
	Version int                     `json:"version"`
	Skills  map[string]cachedSkill  `json:"skills"` // path → cached skill
}

// cachedSkill pairs a file's mtime with its parsed Skill, enabling
// zero-parsing cache hits across process restarts.
type cachedSkill struct {
	MTime time.Time `json:"mtime"`
	Skill Skill     `json:"skill"`
}

// cachePath returns the path to the persistent cache file inside dir.
func cachePath(dir string) string {
	return filepath.Join(dir, cacheFileName)
}

// loadPersistentCache reads the cache file from dir. Returns empty maps
// if the file doesn't exist, has an incompatible version, or is corrupt.
// Never returns an error — degraded behavior is always safe here.
func loadPersistentCache(dir string) (fileCache, map[string]Skill) {
	fileTimes := make(fileCache)
	prevSkills := make(map[string]Skill)

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
		prevSkills[path] = cs.Skill
	}

	return fileTimes, prevSkills
}

// savePersistentCache writes the current fileTimes and prevSkills to disk.
// Errors are silently ignored — the cache is an optimization, not a
// correctness requirement. Atomic write via temp file + rename.
func savePersistentCache(dir string, fc fileCache, prev map[string]Skill) {
	if dir == "" {
		return
	}
	cache := persistentCache{
		Version: cacheVersion,
		Skills:  make(map[string]cachedSkill, len(fc)),
	}
	for path, mtime := range fc {
		if skill, ok := prev[path]; ok {
			cache.Skills[path] = cachedSkill{
				MTime: mtime,
				Skill: skill,
			}
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
