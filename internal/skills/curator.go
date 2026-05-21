package skills

import (
	"fmt"
	"strings"
	"time"
)

// ── Curation ──────────────────────────────────────────────────────────

// CurationReport summarizes the findings of a curation pass.
type CurationReport struct {
	StaleSkills    []Skill        `json:"stale_skills"`
	OverlapGroups  []OverlapGroup `json:"overlap_groups"`
	QualityIssues  []QualityIssue `json:"quality_issues"`
	TotalSkills    int            `json:"total_skills"`
	Deduplicated   int            `json:"deduplicated"`
}

// OverlapGroup groups skills that share trigger keywords and should be merged.
type OverlapGroup struct {
	Skills  []string `json:"skills"` // skill names
	Shared  []string `json:"shared"` // shared topic keywords
	Message string   `json:"message"`
}

// QualityIssue flags a skill that fails structural validation.
type QualityIssue struct {
	Name   string   `json:"name"`
	Issues []string `json:"issues"`
}

// Curate runs all curation passes on a set of skills.
// Passes are read-only unless opts.Apply is set.
type CurateOptions struct {
	StalenessDays int  // skills unused for this many days are flagged
	Apply         bool // apply changes (delete stale if auto_prune enabled)
	Interactive   bool // confirm each change interactively (not used here, set by CLI)
}

// CurateSkills runs the full curation pipeline.
func CurateSkills(skills []Skill, opts CurateOptions) *CurationReport {
	report := &CurationReport{
		TotalSkills: len(skills),
	}

	now := time.Now().UTC()

	// Pass 1: Staleness
	for _, s := range skills {
		if s.LastUsed.IsZero() {
			continue
		}
		daysSinceUse := int(now.Sub(s.LastUsed).Hours() / 24)
		if daysSinceUse >= opts.StalenessDays && s.Quality != QualityManual {
			report.StaleSkills = append(report.StaleSkills, s)
		}
	}

	// Pass 2: Overlap by trigger keyword intersection
	report.OverlapGroups = findOverlapGroups(skills)

	// Pass 3: Quality audit
	for _, s := range skills {
		issues := auditQuality(s)
		if len(issues) > 0 {
			report.QualityIssues = append(report.QualityIssues, QualityIssue{
				Name:   s.Name,
				Issues: issues,
			})
		}
	}

	// Pass 4: Dedup (body hash)
	report.Deduplicated = countDupBodies(skills)

	return report
}

// findOverlapGroups groups skills that share trigger keywords.
func findOverlapGroups(skills []Skill) []OverlapGroup {
	var groups []OverlapGroup
	seen := make(map[string]bool)

	for i, a := range skills {
		if seen[a.Name] {
			continue
		}
		for j, b := range skills {
			if i >= j || seen[b.Name] {
				continue
			}

			// Check trigger keyword overlap
			shared := intersectKeywords(a.Trigger.TopicKeywords, b.Trigger.TopicKeywords)
			if len(shared) >= 2 {
				groups = append(groups, OverlapGroup{
					Skills:  []string{a.Name, b.Name},
					Shared:  shared,
					Message: fmt.Sprintf("Skills share %d topic keywords: %s", len(shared), strings.Join(shared, ", ")),
				})
				seen[a.Name] = true
				seen[b.Name] = true
			}
		}
	}
	return groups
}

// intersectKeywords returns the intersection of two keyword slices.
func intersectKeywords(a, b []string) []string {
	set := make(map[string]bool)
	for _, kw := range a {
		set[kw] = true
	}
	var out []string
	for _, kw := range b {
		if set[kw] {
			out = append(out, kw)
		}
	}
	return out
}

// auditQuality checks a skill's structural completeness.
func auditQuality(s Skill) []string {
	var issues []string

	if !strings.Contains(s.Body, "## Overview") && !strings.Contains(s.Body, "# Overview") {
		issues = append(issues, "missing ## Overview section")
	}
	if !strings.Contains(s.Body, "## Common Pitfalls") {
		issues = append(issues, "missing ## Common Pitfalls section")
	}
	if len(s.Description) > 120 {
		issues = append(issues, fmt.Sprintf("description too long (%d chars, max 120)", len(s.Description)))
	}
	if len(s.Body) < 300 {
		issues = append(issues, fmt.Sprintf("body too short (%d chars, min 300)", len(s.Body)))
	}
	if len(s.Trigger.TopicKeywords) == 0 && len(s.Trigger.ActionKeywords) == 0 && !s.AutoLoad {
		if s.Quality != QualityManual {
			issues = append(issues, "no trigger keywords defined (skill will never auto-load)")
		}
	}
	return issues
}

// countDupBodies counts how many skills share body hashes.
func countDupBodies(skills []Skill) int {
	hashes := make(map[string]int)
	dups := 0
	for _, s := range skills {
		hashes[s.BodyHash]++
		if hashes[s.BodyHash] == 2 {
			dups++
		}
	}
	return dups
}

// ── Post-Session Micro-Curation ──────────────────────────────────────

// MicroCurationResult reports actions taken by MicroCuration.
type MicroCurationResult struct {
	Merged    []string // skill names that were merged (kept, removed)
	Flagged   []string // skills flagged as stale
	Deleted   []string // skills deleted (duplicates, skip-threshold)
	Notes     []string // informational messages
}

// MicroCuration runs lightweight curation after a session.
// Returns a result describing actions taken.
func MicroCuration(userDir string, newSkills []Skill, allSkills []Skill, cfg CurationConfig) *MicroCurationResult {
	result := &MicroCurationResult{}

	// Check for exact duplicates against existing skills
	for _, newS := range newSkills {
		for _, existing := range allSkills {
			if existing.BodyHash == newS.BodyHash && existing.Name != newS.Name {
				result.Merged = append(result.Merged, existing.Name, newS.Name)
				result.Notes = append(result.Notes,
					fmt.Sprintf("duplicate %q has same body as %q", newS.Name, existing.Name))
			}
		}
	}

	// Check for overlapping trigger keywords → flag for merge
	overlaps := findOverlapGroups(allSkills)
	for _, g := range overlaps {
		// Only auto-merge if both are draft quality from self-improvement
		canMerge := true
		for _, name := range g.Skills {
			for _, s := range allSkills {
				if s.Name == name && s.Quality != QualityDraft {
					canMerge = false
					break
				}
			}
		}
		if canMerge && len(g.Skills) >= 2 {
			result.Merged = append(result.Merged, g.Skills...)
			result.Notes = append(result.Notes,
				fmt.Sprintf("overlapping skills share %d keywords: %s",
					len(g.Shared), strings.Join(g.Skills, " + ")))
		}
	}

	// Check staleness
	if cfg.AutoPrune || cfg.AutoCurate {
		now := time.Now().UTC()
		for _, s := range allSkills {
			if s.LastUsed.IsZero() {
				continue
			}
			daysSinceUse := int(now.Sub(s.LastUsed).Hours() / 24)
			if daysSinceUse >= cfg.StalenessDays && s.Quality != QualityManual {
				result.Flagged = append(result.Flagged, s.Name)
				result.Notes = append(result.Notes,
					fmt.Sprintf("flagged %q as stale (%d days unused)", s.Name, daysSinceUse))
				if cfg.AutoPrune {
					result.Deleted = append(result.Deleted, s.Name)
				}
			}
		}
	}

	return result
}

// FormatMicroCurationResult formats a MicroCurationResult for display.
func FormatMicroCurationResult(r *MicroCurationResult) string {
	if r == nil || (len(r.Merged) == 0 && len(r.Flagged) == 0 && len(r.Deleted) == 0) {
		return ""
	}
	var b strings.Builder
	b.WriteString("🔧 Micro-curation: ")
	var parts []string
	if len(r.Merged) > 0 {
		parts = append(parts, fmt.Sprintf("merged %s", strings.Join(r.Merged, " + ")))
	}
	if len(r.Flagged) > 0 {
		parts = append(parts, fmt.Sprintf("flagged %s", strings.Join(r.Flagged, ", ")))
	}
	if len(r.Deleted) > 0 {
		parts = append(parts, fmt.Sprintf("deleted %s", strings.Join(r.Deleted, ", ")))
	}
	b.WriteString(strings.Join(parts, "; "))
	b.WriteString("\n")
	for _, note := range r.Notes {
		b.WriteString(fmt.Sprintf("   %s\n", note))
	}
	return b.String()
}

// ── Format Report ─────────────────────────────────────────────────────

// FormatCurationReport formats a CurationReport for display.
func FormatCurationReport(r *CurationReport) string {
	var b strings.Builder

	b.WriteString("📦 Skill Curation Report\n")
	b.WriteString("━━━━━━━━━━━━━━━━━━━━━━━━\n")
	b.WriteString(fmt.Sprintf("Total skills analyzed: %d\n\n", r.TotalSkills))

	if len(r.StaleSkills) > 0 {
		b.WriteString(fmt.Sprintf("⚠  Stale skills (%d):\n", len(r.StaleSkills)))
		for _, s := range r.StaleSkills {
			b.WriteString(fmt.Sprintf("   %-20s → run with --apply to mark stale\n", s.Name))
		}
		b.WriteString("\n")
	}

	if len(r.OverlapGroups) > 0 {
		b.WriteString(fmt.Sprintf("🔗  Overlap groups (%d):\n", len(r.OverlapGroups)))
		for _, g := range r.OverlapGroups {
			b.WriteString(fmt.Sprintf("   %s\n", strings.Join(g.Skills, " + ")))
			b.WriteString(fmt.Sprintf("     %s\n", g.Message))
		}
		b.WriteString("\n")
	}

	if len(r.QualityIssues) > 0 {
		b.WriteString(fmt.Sprintf("📋  Quality issues (%d):\n", len(r.QualityIssues)))
		for _, qi := range r.QualityIssues {
			b.WriteString(fmt.Sprintf("   %s:\n", qi.Name))
			for _, issue := range qi.Issues {
				b.WriteString(fmt.Sprintf("     - %s\n", issue))
			}
		}
		b.WriteString("\n")
	}

	if r.Deduplicated > 0 {
		b.WriteString(fmt.Sprintf("🔍  Deduplicated: %d skills share body hashes\n", r.Deduplicated))
	}

	b.WriteString("\nRun `odek skill curate --apply` to apply all suggestions\n")
	b.WriteString("Run `odek skill curate --interactive` to review one-by-one")

	return b.String()
}
