package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ── Frontmatter Parsing ───────────────────────────────────────────────
//
// Manual YAML frontmatter parser for the SKILL.md subset:
//   - Scalar key: value pairs (strings)
//   - Nested maps (up to 3 levels: key / subkey / subsubkey)
//   - Booleans (true/false)
//   - Integers
//   - No arrays, no multi-line strings, no anchors/aliases

// parseSkillFile reads and parses a single SKILL.md file.
// Returns nil if the file doesn't exist or can't be parsed.
func parseSkillFile(path string) *Skill {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return parseSkillContent(string(data), path)
}

// parseSkillContent parses SKILL.md content from a string.
func parseSkillContent(content, sourcePath string) *Skill {
	content = strings.TrimSpace(content)
	if !strings.HasPrefix(content, "---") {
		return nil
	}

	// Find closing ---
	rest := content[3:]
	idx := strings.Index(rest, "\n---")
	if idx < 0 {
		return nil
	}

	frontmatter := rest[:idx]
	body := strings.TrimSpace(rest[idx+4:]) // skip past \n---\n

	if body == "" {
		return nil
	}

	// Parse frontmatter into a nested map
	fm := parseYAMLMap(frontmatter)
	if fm == nil {
		return nil
	}

	name, _ := fm["name"].(string)
	if name == "" {
		return nil
	}
	if err := ValidateSkillName(name); err != nil {
		return nil // reject names with path traversal at load time
	}

	desc, _ := fm["description"].(string)
	version, _ := fm["version"].(string)
	author, _ := fm["author"].(string)

	// Parse kode section
	var trigger SkillTrigger
	autoLoad := false
	quality := QualityManual

	if kode, ok := fm["kode"].(map[string]any); ok {
		if t, ok := kode["trigger"].(map[string]any); ok {
			topic, _ := t["topic"].(string)
			action, _ := t["action"].(string)
			trigger = SkillTrigger{
				TopicKeywords:  splitKeywords(topic),
				ActionKeywords: splitKeywords(action),
			}
		}
		if al, ok := kode["auto_load"].(bool); ok {
			autoLoad = al
		}
		if q, ok := kode["quality"].(string); ok {
			quality = parseQualityFlag(q)
		}
	} else {
		// Derive keywords from body if no trigger section
		topics, actions := DeriveKeywords(body)
		trigger = SkillTrigger{
			TopicKeywords:  topics,
			ActionKeywords: actions,
		}
	}

	return &Skill{
		Name:        name,
		Description: desc,
		Version:     version,
		Author:      author,
		AutoLoad:    autoLoad,
		Quality:     quality,
		Trigger:     trigger,
		Body:        body,
		BodyHash:    HashBody(body),
	}
}

// parseYAMLMap parses a simple YAML key/value block into a nested map.
// Supports:
//
//	key: value
//	key:
//	  subkey: value
//	  subkey2:
//	    subsub: value
func parseYAMLMap(s string) map[string]any {
	lines := strings.Split(s, "\n")
	result := make(map[string]any)
	var stack []map[string]any
	var stackIndent []int

	stack = append(stack, result)
	stackIndent = append(stackIndent, -1)

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Calculate indent level (2 spaces per level)
		indent := 0
		for _, ch := range line {
			if ch == ' ' {
				indent++
			} else {
				break
			}
		}

		// Pop stack to the right indent level
		for len(stack) > 1 && indent <= stackIndent[len(stackIndent)-1] {
			stack = stack[:len(stack)-1]
			stackIndent = stackIndent[:len(stackIndent)-1]
		}

		// Split key: value at first colon-space or colon-newline
		colonIdx := strings.Index(trimmed, ":")
		if colonIdx < 0 {
			continue
		}

		key := strings.TrimSpace(trimmed[:colonIdx])
		rest := strings.TrimSpace(trimmed[colonIdx+1:])

		current := stack[len(stack)-1]

		if rest == "" {
			// Nested map
			nested := make(map[string]any)
			current[key] = nested
			stack = append(stack, nested)
			stackIndent = append(stackIndent, indent)
		} else {
			// Scalar value — parse as appropriate type
			current[key] = parseYAMLValue(rest)
		}
	}

	return result
}

// parseYAMLValue converts a string to its inferred Go type.
func parseYAMLValue(s string) any {
	// Bool
	if s == "true" || s == "yes" || s == "on" {
		return true
	}
	if s == "false" || s == "no" || s == "off" {
		return false
	}
	// Quoted string
	if (strings.HasPrefix(s, "\"") && strings.HasSuffix(s, "\"")) ||
		(strings.HasPrefix(s, "'") && strings.HasSuffix(s, "'")) {
		return s[1 : len(s)-1]
	}
	// Integer
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	// Float
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	// String (default)
	return s
}

// parseQualityFlag converts a string to SkillQuality.
func parseQualityFlag(s string) SkillQuality {
	switch strings.TrimSpace(s) {
	case "draft":
		return QualityDraft
	case "verified":
		return QualityVerified
	case "imported":
		return QualityImported
	case "manual":
		return QualityManual
	case "stale":
		return QualityStale
	default:
		return QualityManual
	}
}

// splitKeywords splits a whitespace-separated string into keywords.
func splitKeywords(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Fields(s)
}

// ── Loader ────────────────────────────────────────────────────────────

// ScanResult holds the result of scanning skill directories.
type ScanResult struct {
	AutoLoad []Skill // skills with auto_load=true
	Lazy     []Skill // skills with auto_load=false
}

// ScanDirs scans the project-local and user-global skill directories,
// plus any additional dirs, and returns categorized skills.
// Dirs are scanned in order: project → user → extras.
// If a skill name exists in multiple dirs, the first (higher-priority) wins.
func ScanDirs(projectDir, userDir string, extraDirs []string) *ScanResult {
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
		skills := scanDir(dir)
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

// scanDir reads all SKILL.md files in a single skill directory.
func scanDir(dir string) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	var skills []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillPath := filepath.Join(dir, e.Name(), "SKILL.md")
		s := parseSkillFile(skillPath)
		if s == nil {
			continue
		}
		s.Source = SkillSource{
			Dir:  dir,
			Path: skillPath,
		}
		s.LastUsed = time.Now().UTC()
		skills = append(skills, *s)
	}
	return skills
}

// ── Formatting ────────────────────────────────────────────────────────

// FormatAsContext formats a skill's body for injection into the system prompt.
func FormatAsContext(s Skill) string {
	var b strings.Builder
	b.WriteString("## Skill: ")
	b.WriteString(s.Name)
	b.WriteString("\n\n")
	b.WriteString(s.Body)
	b.WriteString("\n---\n")
	return b.String()
}

// ── Writing ───────────────────────────────────────────────────────────

// WriteSkill writes a skill to the given directory as <name>/SKILL.md.
// Creates the directory if it doesn't exist. Returns an error if the
// skill name is unsafe for filesystem use (path traversal, etc.).
func WriteSkill(dir string, s Skill) error {
	if err := ValidateSkillName(s.Name); err != nil {
		return fmt.Errorf("write skill: %w", err)
	}
	skillDir := filepath.Join(dir, s.Name)
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		return fmt.Errorf("create skill dir: %w", err)
	}

	content := MarshalSkill(s)
	path := filepath.Join(skillDir, "SKILL.md")
	return os.WriteFile(path, []byte(content), 0644)
}

// ── Serialization ─────────────────────────────────────────────────────

// MarshalSkill serializes a skill to its SKILL.md representation.
func MarshalSkill(s Skill) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("name: %s\n", s.Name))
	if s.Description != "" {
		b.WriteString(fmt.Sprintf("description: %s\n", s.Description))
	}
	if s.Version != "" {
		b.WriteString(fmt.Sprintf("version: %s\n", s.Version))
	}
	if s.Author != "" {
		b.WriteString(fmt.Sprintf("author: %s\n", s.Author))
	}
	b.WriteString("kode:\n")
	if len(s.Trigger.TopicKeywords) > 0 || len(s.Trigger.ActionKeywords) > 0 {
		b.WriteString("  trigger:\n")
		if len(s.Trigger.TopicKeywords) > 0 {
			b.WriteString(fmt.Sprintf("    topic: %s\n", strings.Join(s.Trigger.TopicKeywords, " ")))
		}
		if len(s.Trigger.ActionKeywords) > 0 {
			b.WriteString(fmt.Sprintf("    action: %s\n", strings.Join(s.Trigger.ActionKeywords, " ")))
		}
	}
	if s.AutoLoad {
		b.WriteString("  auto_load: true\n")
	}
	if s.Quality != "" && s.Quality != QualityManual {
		b.WriteString(fmt.Sprintf("  quality: %s\n", s.Quality))
	}
	b.WriteString("---\n\n")
	b.WriteString(s.Body)
	if !strings.HasSuffix(s.Body, "\n") {
		b.WriteString("\n")
	}
	return b.String()
}
