package skills

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BackendStack21/odek/internal/fsatomic"
)

// promotedRegistryFile is the trusted-side promotion registry, stored in
// the operator-controlled user skills dir. The project-local skills dir is
// attacker-controllable (a cloned repo can ship arbitrary SKILL.md files),
// so trust cannot be licensed by anything written there — including the
// frontmatter `odek skill promote` used to clear. Promotions are anchored
// here instead, keyed by the exact file content hash: any later edit to
// the skill re-locks it to NeedsReview until re-promoted.
const promotedRegistryFile = "promoted.json"

func promotedRegistryPath(userDir string) string {
	return filepath.Join(userDir, promotedRegistryFile)
}

// RecordPromotion records the content hash of a promoted project skill in
// the trusted user dir. Fails-safe: a corrupt or unwritable registry is an
// error (the caller surfaces it) — never a silent promotion.
func RecordPromotion(userDir, name string, content []byte) error {
	if userDir == "" {
		return fmt.Errorf("skills: promotion registry requires a user skills dir")
	}
	sum := sha256.Sum256(content)
	path := promotedRegistryPath(userDir)
	reg := map[string]string{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &reg); err != nil {
			// Corrupt registry: start fresh — every project skill re-locks
			// until re-promoted (fail-safe direction).
			reg = map[string]string{}
		}
	}
	reg[name] = hex.EncodeToString(sum[:])
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return fmt.Errorf("skills: marshal promotion registry: %w", err)
	}
	if err := os.MkdirAll(userDir, 0755); err != nil {
		return fmt.Errorf("skills: create promotion registry dir: %w", err)
	}
	if err := fsatomic.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("skills: write promotion registry: %w", err)
	}
	return nil
}

// isPromotedContent reports whether this exact skill file content was
// operator-promoted (hash recorded in the trusted registry).
func isPromotedContent(userDir, name string, content []byte) bool {
	if userDir == "" {
		return false
	}
	data, err := os.ReadFile(promotedRegistryPath(userDir))
	if err != nil {
		return false
	}
	reg := map[string]string{}
	if err := json.Unmarshal(data, &reg); err != nil {
		return false
	}
	sum := sha256.Sum256(content)
	return reg[name] == hex.EncodeToString(sum[:])
}
