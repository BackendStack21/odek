package telegram

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ── Plan Manager ───────────────────────────────────────────────────────
//
// Plans are stored as .md files in ~/.odek/plans/. Each plan is a
// markdown file named <slug>.md where the slug is derived from the
// user's description. The PlanManager provides CRUD operations:
//
//   ListPlans   — enumerate plans sorted by modification time (newest first)
//   ReadPlan    — load a plan by slug prefix match
//   DeletePlan  — remove a plan by slug prefix match
//   MostRecentPlan — returns the most recently modified plan's content
//
// Slug generation collapses the description into a lowercase, hyphen-
// separated identifier (max 60 chars, alphanumeric + hyphens only).

// PlanInfo is a lightweight summary of a plan file.
type PlanInfo struct {
	Slug    string    // filename without .md extension
	Path    string    // full filesystem path
	ModTime time.Time // last modification time
	Preview string    // first line or ~80 chars of content
}

// plansDir returns the plans directory path (~/.odek/plans/).
func plansDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("plans: home dir: %w", err)
	}
	return filepath.Join(home, ".odek", "plans"), nil
}

// ensurePlansDir creates the plans directory if it doesn't exist.
func ensurePlansDir() (string, error) {
	dir, err := plansDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("plans: mkdir: %w", err)
	}
	return dir, nil
}

// Slugify converts a description into a filesystem-safe slug.
// Rules: lowercase, max 60 chars, only [a-z0-9] and hyphens,
// multiple hyphens collapsed, no leading/trailing hyphens.
func Slugify(desc string) string {
	return slugify(desc)
}

// slugify is the internal implementation.
func slugify(desc string) string {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return "plan"
	}

	// Lowercase and limit length.
	runes := []rune(strings.ToLower(desc))
	if len(runes) > 60 {
		runes = runes[:60]
	}

	var b strings.Builder
	var lastHyphen bool
	for _, r := range runes {
		if r <= 127 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			lastHyphen = false
		} else if !lastHyphen {
			b.WriteRune('-')
			lastHyphen = true
		}
	}

	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "plan"
	}
	return slug
}

// planPath returns the full path for a plan file given its slug.
func planPath(slug string) (string, error) {
	dir, err := plansDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, slug+".md"), nil
}

// ── CRUD ────────────────────────────────────────────────────────────────

// ListPlans returns all .md plan files sorted by modification time
// (newest first). If limit > 0, only the most recent `limit` plans are
// returned. Returns an empty slice if the plans directory doesn't exist.
func ListPlans(limit int) ([]PlanInfo, error) {
	dir, err := plansDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list plans: read dir: %w", err)
	}

	infos := make([]PlanInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".md")
		path := filepath.Join(dir, e.Name())
		preview := ""
		if data, err := os.ReadFile(path); err == nil {
			preview = firstLine(string(data), 80)
		}
		infos = append(infos, PlanInfo{
			Slug:    slug,
			Path:    path,
			ModTime: fi.ModTime(),
			Preview: preview,
		})
	}

	// Sort newest first.
	sort.Slice(infos, func(i, j int) bool {
		return infos[i].ModTime.After(infos[j].ModTime)
	})

	if limit > 0 && len(infos) > limit {
		infos = infos[:limit]
	}

	return infos, nil
}

// ReadPlan loads a plan by slug prefix match. Returns the slug, content,
// and any error. If multiple plans match the prefix, the first exact match
// is preferred, then the first prefix match. Returns an error if no match
// is found.
func ReadPlan(slugPrefix string) (string, string, error) {
	slugPrefix = strings.ToLower(strings.TrimSpace(slugPrefix))
	if slugPrefix == "" {
		return "", "", fmt.Errorf("plan slug required")
	}

	dir, err := plansDir()
	if err != nil {
		return "", "", err
	}

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return "", "", fmt.Errorf("no plans directory found")
	}
	if err != nil {
		return "", "", fmt.Errorf("read plan: %w", err)
	}

	// Collect matching slugs.
	var exactMatch string
	var prefixMatches []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".md")
		if strings.EqualFold(slug, slugPrefix) {
			exactMatch = slug
			break
		}
		if strings.HasPrefix(strings.ToLower(slug), slugPrefix) {
			prefixMatches = append(prefixMatches, slug)
		}
	}

	match := exactMatch
	if match == "" && len(prefixMatches) == 1 {
		match = prefixMatches[0]
	} else if match == "" && len(prefixMatches) > 1 {
		return "", "", fmt.Errorf("multiple plans match %q: %s", slugPrefix,
			strings.Join(prefixMatches, ", "))
	} else if match == "" {
		return "", "", fmt.Errorf("no plan matching %q found — use /plans to list", slugPrefix)
	}

	data, err := os.ReadFile(filepath.Join(dir, match+".md"))
	if err != nil {
		return "", "", fmt.Errorf("read plan %q: %w", match, err)
	}
	return match, string(data), nil
}

// DeletePlan removes a plan file by slug prefix match. Uses the same
// matching logic as ReadPlan. Returns the slug of the deleted plan.
func DeletePlan(slugPrefix string) (string, error) {
	slugPrefix = strings.ToLower(strings.TrimSpace(slugPrefix))
	if slugPrefix == "" {
		return "", fmt.Errorf("plan slug required")
	}

	dir, err := plansDir()
	if err != nil {
		return "", err
	}

	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return "", fmt.Errorf("no plans directory found")
	}
	if err != nil {
		return "", fmt.Errorf("delete plan: %w", err)
	}

	var exactMatch string
	var prefixMatches []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		slug := strings.TrimSuffix(e.Name(), ".md")
		if strings.EqualFold(slug, slugPrefix) {
			exactMatch = slug
			break
		}
		if strings.HasPrefix(strings.ToLower(slug), slugPrefix) {
			prefixMatches = append(prefixMatches, slug)
		}
	}

	match := exactMatch
	if match == "" && len(prefixMatches) == 1 {
		match = prefixMatches[0]
	} else if match == "" && len(prefixMatches) > 1 {
		return "", fmt.Errorf("multiple plans match %q: %s — be more specific",
			slugPrefix, strings.Join(prefixMatches, ", "))
	} else if match == "" {
		return "", fmt.Errorf("no plan matching %q found", slugPrefix)
	}

	path := filepath.Join(dir, match+".md")
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("delete plan %q: %w", match, err)
	}
	return match, nil
}

// MostRecentPlan returns the slug and full content of the most recently
// modified plan file. Returns an error if no plans exist.
func MostRecentPlan() (string, string, error) {
	infos, err := ListPlans(1)
	if err != nil {
		return "", "", err
	}
	if len(infos) == 0 {
		return "", "", fmt.Errorf("no plans found — create one with /plan <description>")
	}

	data, err := os.ReadFile(infos[0].Path)
	if err != nil {
		return "", "", fmt.Errorf("read plan: %w", err)
	}
	return infos[0].Slug, string(data), nil
}

// ── Helpers ─────────────────────────────────────────────────────────────

// firstLine returns the first non-empty line of text, truncated to maxLen.
func firstLine(text string, maxLen int) string {
	lines := strings.SplitN(text, "\n", 2)
	first := strings.TrimSpace(lines[0])
	// Skip markdown headings prefix.
	first = strings.TrimLeft(first, "# ")
	if len(first) > maxLen {
		return first[:maxLen] + "…"
	}
	return first
}
