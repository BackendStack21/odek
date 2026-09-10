package memory

import (
	"context"
	"fmt"
	"os"
	"slices"
)

// ConsolidationPreview is a proposed replacement, never applied implicitly.
type ConsolidationPreview struct {
	Before []string `json:"before"`
	After  []string `json:"after"`
}

// PreviewConsolidation runs the existing consolidation/scanning/cap checks in an
// isolated temporary fact store, leaving live facts available to other sessions.
func (m *MemoryManager) PreviewConsolidation(target string) (ConsolidationPreview, error) {
	var result ConsolidationPreview
	if target != "user" && target != "env" {
		return result, fmt.Errorf("invalid memory target")
	}
	unlock, err := lockFactsDir(m.facts.dir)
	if err != nil {
		return result, err
	}
	result.Before, err = m.facts.Entries(target)
	unlock()
	if err != nil {
		return result, err
	}
	dir, err := os.MkdirTemp("", "odek-memory-preview-")
	if err != nil {
		return result, err
	}
	defer os.RemoveAll(dir)
	clone := NewMemoryManager(dir, m.llm, m.cfg)
	clone.guard = m.guard
	clone.guardCfg = m.guardCfg
	if err = clone.facts.writeEntries(target, result.Before); err != nil {
		return result, err
	}
	if err = clone.Consolidate(target); err != nil {
		return result, err
	}
	result.After, err = clone.facts.Entries(target)
	return result, err
}

// ApplyConsolidation only replaces the exact reviewed snapshot. Concurrent fact
// updates cause a conflict instead of being silently overwritten by a preview.
func (m *MemoryManager) ApplyConsolidation(target string, preview ConsolidationPreview) error {
	if target != "user" && target != "env" {
		return fmt.Errorf("invalid memory target")
	}
	if len(preview.After) == 0 {
		return fmt.Errorf("empty consolidation is not allowed")
	}
	if m.facts.sizeOf(preview.After) > m.facts.cap(target) {
		return fmt.Errorf("consolidated facts exceed character cap")
	}
	for _, entry := range preview.After {
		if err := m.scanContent(context.Background(), entry); err != nil {
			return err
		}
	}
	unlock, err := lockFactsDir(m.facts.dir)
	if err != nil {
		return err
	}
	defer unlock()
	current, err := m.facts.Entries(target)
	if err != nil {
		return err
	}
	if !slices.Equal(current, preview.Before) {
		return fmt.Errorf("memory changed since preview; generate a new preview")
	}
	if err = m.facts.writeEntries(target, preview.After); err != nil {
		return err
	}
	m.markPromptDirty()
	return nil
}
