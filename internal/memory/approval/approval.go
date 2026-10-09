// Package approval renders what a mutating memory tool call will persist, for
// the approval prompts (terminal, WebSocket, Telegram) and the agent loop's
// batch card. A memory fact or atom is injected into every future system
// prompt, so the human approving the write must read the text being stored,
// the same way a shell command is shown in full. The package is a leaf (it
// depends only on internal/danger) so the agent loop can use it without
// importing the memory manager.
package approval

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/danger"
)

// MaxTextBytes bounds every model-supplied field of a mutating memory call
// (content, old_text, target, ids). Approvals show each field in full —
// nothing is elided, so no part of a fact can hide from the human approving
// it — and a call with a longer field is refused (see CheckBounds) so the
// model splits it into smaller entries. The bound sits well under
// danger.InlineMaxBytes, so the display sanitiser never truncates either.
// old_text is a unique substring of the entry it targets, not the whole
// entry, so a stored fact longer than the bound (written before it existed)
// stays replaceable and removable through a short, unique old_text; the
// memory tool's confirmation shows the entry itself (ResourceWithEntry).
const MaxTextBytes = 2048

// CheckBounds refuses a mutating memory call whose fields exceed
// MaxTextBytes. The memory tool calls it before the approval prompt.
func CheckBounds(a Args) error {
	for _, f := range []struct{ name, v string }{
		{"content", a.Content}, {"old_text", a.OldText}, {"target", a.Target},
		{"atom_id", a.AtomID}, {"pending_id", a.PendingID}, {"atom_type", a.AtomType},
	} {
		if len(f.v) > MaxTextBytes {
			return fmt.Errorf("%s is %d bytes; memory writes are limited to %d bytes per field so the approval can show them in full — split it into smaller entries", f.name, len(f.v), MaxTextBytes)
		}
	}
	return nil
}

// Args holds the memory tool arguments that identify what a mutation writes
// or removes.
type Args struct {
	Action    string `json:"action"`
	Target    string `json:"target"`
	Content   string `json:"content"`
	OldText   string `json:"old_text"`
	AtomID    string `json:"atom_id"`
	PendingID string `json:"pending_id"`
	AtomType  string `json:"atom_type"`
}

// Mutates reports whether a memory action changes persisted memory and
// therefore needs persistence approval.
func Mutates(action string) bool {
	switch action {
	case "add", "replace", "remove", "consolidate",
		"add_atom", "forget_atom", "pin_atom",
		"confirm_pending_review", "reject_pending_review":
		return true
	default:
		return false
	}
}

// ResourceFromArgs parses raw memory tool arguments and returns the approval
// resource. ok is false when the arguments do not parse or the action does
// not mutate memory.
func ResourceFromArgs(raw string) (resource string, ok bool) {
	var a Args
	if err := json.Unmarshal([]byte(raw), &a); err != nil || !Mutates(a.Action) {
		return "", false
	}
	return Resource(a), true
}

// Resource returns the single-line approval resource for a memory mutation:
// the action, its target, and the text being stored (add, replace, add_atom)
// or the entry being removed or pinned (remove, forget_atom, pin_atom,
// pending-review decisions). Every model-supplied field is sanitised with
// danger.SanitizeInline and shown in full. A call that CheckBounds refuses is
// rendered with a visible "refused" marker instead of its oversized field.
func Resource(a Args) string {
	if err := CheckBounds(a); err != nil {
		return fmt.Sprintf("memory %s (refused: %s)", field(a.Action), danger.SanitizeInline(err.Error()))
	}
	action := field(a.Action)
	switch a.Action {
	case "add":
		return fmt.Sprintf("memory add %s: %s", field(a.Target), quoted(a.Content))
	case "replace":
		return fmt.Sprintf("memory replace %s: %s → %s (matched entry shown at confirmation)", field(a.Target), quoted(a.OldText), quoted(a.Content))
	case "remove":
		return fmt.Sprintf("memory remove %s: %s (matched entry shown at confirmation)", field(a.Target), quoted(a.OldText))
	case "consolidate":
		return fmt.Sprintf("memory consolidate %s", field(a.Target))
	case "add_atom":
		typ := a.AtomType
		if typ == "" {
			typ = "fact"
		}
		return fmt.Sprintf("memory add_atom %s: %s", field(typ), quoted(a.Content))
	case "forget_atom", "pin_atom":
		return fmt.Sprintf("memory %s %s", action, field(a.AtomID))
	case "confirm_pending_review", "reject_pending_review":
		return fmt.Sprintf("memory %s %s", action, field(a.PendingID))
	default:
		return "memory " + action
	}
}

// EntryExcerptBytes is how much of an existing entry longer than
// MaxTextBytes the approval shows, after its length and SHA-256.
const EntryExcerptBytes = 512

// ResourceWithEntry is Resource for a replace or remove whose old_text has
// been resolved to the stored entry it selects. The approval shows that
// whole entry (old_text may be a short substring of it), sanitised. An entry
// longer than MaxTextBytes — stored before the bound existed — is identified
// by its length and SHA-256 with a leading excerpt marked as truncated. The
// new content of a replace is always shown in full; CheckBounds bounds it.
func ResourceWithEntry(a Args, entry string) string {
	if err := CheckBounds(a); err != nil {
		return Resource(a)
	}
	switch a.Action {
	case "replace":
		return fmt.Sprintf("memory replace %s: entry %s (selected by %s) → %s", field(a.Target), entryText(entry), quoted(a.OldText), quoted(a.Content))
	case "remove":
		return fmt.Sprintf("memory remove %s: entry %s (selected by %s)", field(a.Target), entryText(entry), quoted(a.OldText))
	default:
		return Resource(a)
	}
}

// entryText renders a stored entry for an approval: in full when it fits the
// display bound, otherwise as length, SHA-256 and a marked leading excerpt.
func entryText(entry string) string {
	if len(entry) <= MaxTextBytes {
		return quoted(entry)
	}
	cut := EntryExcerptBytes
	for cut > 0 && !utf8.RuneStart(entry[cut]) {
		cut--
	}
	sum := sha256.Sum256([]byte(entry))
	return fmt.Sprintf("[%d bytes, sha256:%s — longer than the %d-byte display bound, truncated: only the first %d bytes are shown] %s…",
		len(entry), hex.EncodeToString(sum[:]), MaxTextBytes, cut, quoted(entry[:cut]))
}

// field sanitises a short identifier-like value (target, id, type).
func field(s string) string {
	return danger.SanitizeInline(s)
}

// quoted sanitises a text value and wraps it in quotes.
func quoted(s string) string {
	return `"` + danger.SanitizeInline(s) + `"`
}
