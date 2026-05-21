package skills

import "time"

// ── Skill Event ─────────────────────────────────────────────────────────

// SkillEvent represents a skill lifecycle event emitted by the SkillManager.
// Callers (Terminal, WebUI, Telegram) consume these events to surface
// skill activity to the user.
type SkillEvent struct {
	Type      string    // "loaded", "autoloaded", "suggested", "saved", "deleted", "used"
	SkillName string    // single skill name (for saved/deleted/used)
	Skills    []string  // list of skill names (for batch load events)
	Heuristic string    // for "suggested" events (multi-step, error-recovery, etc.)
	Timestamp time.Time // when the event occurred (UTC)
}

// ── SkillNotifier Interface ─────────────────────────────────────────────

// SkillNotifier is the observer interface for skill lifecycle events.
// Implementations should be non-blocking in the hot path (skill loading
// fires mid-loop); use channel-based or async dispatch for I/O.
type SkillNotifier interface {
	Notify(event SkillEvent)
}

// ── NoopNotifier ────────────────────────────────────────────────────────

// NoopNotifier is a SkillNotifier that discards all events.
// Used as the default when no notifier is configured.
type NoopNotifier struct{}

// Notify discards the event.
func (NoopNotifier) Notify(event SkillEvent) {}

// ── MultiNotifier ───────────────────────────────────────────────────────

// MultiNotifier fans out each Notify call to all registered notifiers.
// Safe for concurrent use when the notifier slice is set before any
// calls to Notify (which is the pattern: constructed, set on SkillManager,
// then called from the agent loop).
type MultiNotifier struct {
	notifiers []SkillNotifier
}

// NewMultiNotifier creates a MultiNotifier that fans out to the given notifiers.
func NewMultiNotifier(notifiers ...SkillNotifier) *MultiNotifier {
	return &MultiNotifier{notifiers: notifiers}
}

// Notify fans out the event to all registered notifiers.
// Returns immediately after the last notifier completes.
func (m *MultiNotifier) Notify(event SkillEvent) {
	for _, n := range m.notifiers {
		n.Notify(event)
	}
}
