package danger

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// Approver is the interface for user approval of dangerous operations.
// Two implementations exist:
//
//   - TTYApprover — opens /dev/tty for interactive approval (CLI mode)
//   - WSApprover  — sends approval requests via WebSocket (serve mode)
//
// When nil (no approver configured), calls fall back to non-interactive
// behavior (NonInteractiveAction). Tools MUST inject an approver to get
// interactive approval in any mode.
type Approver interface {
	// PromptCommand asks the user to approve or deny a shell command.
	// cls is the risk class (system_write, network_egress, etc.).
	// Returns nil on approve, error on deny or timeout.
	PromptCommand(cls RiskClass, cmd, description string) error

	// PromptOperation asks the user to approve or deny a native tool operation
	// (read_file on /etc, browser to external URL, etc.).
	PromptOperation(op ToolOperation) error
}

// ToolBatchClass is the synthetic risk class the agent loop uses when it
// prompts once for a batch of tool calls together. A batch aggregates
// multiple tools whose individual classes may differ, so it must never be
// session-trustable: one Trust grant would blanket-approve the contents of
// every later batch (and, via the loop's SetTrustAll, every per-tool prompt
// in the session).
const ToolBatchClass = RiskClass("tool_batch")

// TrustShortcutAllowed reports whether cls may be session-trusted via the
// "trust" shortcut. Destructive, Blocked, and Unknown must never be
// (fail-closed catch-alls; blanket-trusting them is carte blanche), and
// neither may ToolBatchClass — see its doc comment. Persistence is also
// excluded: its writes execute later, outside the session where the trust
// was granted, so a one-time "trust" must not cover every future hook,
// profile, and CI-workflow write (H-5). UnreadExec is excluded because the
// entire point of the gate is per-script review — trusting it once would
// blanket-approve every unread script for the session (H-6).
func TrustShortcutAllowed(cls RiskClass) bool {
	return cls != Destructive && cls != Blocked && cls != Unknown &&
		cls != ToolBatchClass && cls != Persistence && cls != UnreadExec
}

// readToolNames are the native tools whose entire effect on their target is
// inspection. Used by the read_only non-interactive fallback (H-7) — the
// description parameter of PromptOperation carries the tool name.
var readToolNames = map[string]bool{
	"read_file": true, "batch_read": true, "search_files": true, "glob": true,
	"file_info": true, "tree": true, "diff": true, "multi_grep": true,
	"json_query": true, "count_lines": true, "checksum": true, "sort": true,
	"head_tail": true, "base64": true, "tr": true, "word_count": true,
	"transcribe": true, "session_search": true,
}

func isReadToolName(name string) bool {
	return readToolNames[name]
}

var (
	// ttyPromptMu serializes all TTY approval prompts process-wide. Without
	// this, concurrent tool calls (e.g. parallel_shell) each open /dev/tty
	// with their own bufio.Reader and compete for keystrokes, so the user
	// can approve a command they never saw.
	ttyPromptMu sync.Mutex

	// ttyApprovalLog and ttyApprovalMu track approvals across all
	// TTYApprover instances, so friction mode engages even when tools
	// create fresh approvers per prompt.
	ttyApprovalMu  sync.Mutex
	ttyApprovalLog = make(map[RiskClass][]time.Time)
)

// ResetTTYFrictionStateForTest clears the process-wide approval log used by
// friction mode. It is intended for tests that need a clean approval baseline.
func ResetTTYFrictionStateForTest() {
	ttyApprovalMu.Lock()
	ttyApprovalLog = make(map[RiskClass][]time.Time)
	ttyApprovalMu.Unlock()
}

// TTYApprover implements Approver by reading from /dev/tty.
// This is the default approver used in CLI mode (odek run, odek repl).
// When /dev/tty is not available (piped stdin, CI), it falls back to
// the configured NonInteractiveAction.
type TTYApprover struct {
	DangerousConfig *DangerousConfig
	TrustedClasses  map[RiskClass]bool
	mu              sync.Mutex
	TTYPath         string // overridden in tests
	trustAll        bool   // when true, all PromptCommand calls auto-approve

	// Approval-fatigue mitigation. After FrictionThreshold approvals of
	// the same class within FrictionWindow, the next prompt requires
	// the user to type the literal word "approve" (no single-letter
	// shortcut) and prints a 1.5s pause before accepting input. This
	// breaks reflexive click-through and gives the user a moment to
	// notice they have approved an unusual number of dangerous calls.
	FrictionThreshold int
	FrictionWindow    time.Duration
	// pauseFn is overridden in tests so we don't actually sleep.
	pauseFn func(d time.Duration)
}

// NewTTYApprover creates a TTYApprover with the given config.
func NewTTYApprover(cfg *DangerousConfig) *TTYApprover {
	// Tests run many TTYApprover-backed assertions in the same process.
	// Reset the process-wide approval log on each creation so friction-mode
	// tests start from a known baseline. Production keeps the global log so
	// friction engages across tool instances.
	if testing.Testing() {
		ResetTTYFrictionStateForTest()
	}
	return &TTYApprover{
		DangerousConfig:   cfg,
		TrustedClasses:    make(map[RiskClass]bool),
		TTYPath:           ttyPath(),
		FrictionThreshold: 3,
		FrictionWindow:    60 * time.Second,
		pauseFn:           func(d time.Duration) { time.Sleep(d) },
	}
}

// ttyPathForTest, when non-empty, replaces the default /dev/tty for every
// TTYApprover created afterwards.
var ttyPathForTest string

// SetTTYPathForTest overrides the device path TTYApprover opens for
// interactive approvals. Test-only: a test process running on an operator's
// dev machine has a real controlling terminal, so any tool call that lands
// in the Prompt fallback would render a live approval prompt and block on
// the operator's keyboard forever (observed 2026-08-30 with browser tests on
// macOS). Pointing the path at a nonexistent device makes every fallback
// open fail, which engages the documented NonInteractiveAction fallback —
// exactly the semantics the non-interactive tests were authored against.
// Tests that script their own TTY set TTYPath directly after construction
// and are unaffected.
func SetTTYPathForTest(path string) { ttyPathForTest = path }

// ttyPath returns the device path TTYApprover should open.
func ttyPath() string {
	if ttyPathForTest != "" {
		return ttyPathForTest
	}
	return "/dev/tty"
}

// recordApproval logs an approval timestamp for the given class and
// returns true if the next prompt for this class should engage the
// high-friction path.
func (a *TTYApprover) recordApproval(cls RiskClass) {
	ttyApprovalMu.Lock()
	defer ttyApprovalMu.Unlock()
	ttyApprovalLog[cls] = append(ttyApprovalLog[cls], time.Now())
}

// prunedApprovalCountLocked prunes approvals of cls that fell outside the
// friction window and returns how many remain. Caller must hold ttyApprovalMu.
func (a *TTYApprover) prunedApprovalCountLocked(cls RiskClass) int {
	cutoff := time.Now().Add(-a.FrictionWindow)
	log := ttyApprovalLog[cls]
	kept := log[:0]
	for _, t := range log {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	ttyApprovalLog[cls] = kept
	return len(kept)
}

// shouldFriction returns true when there have been >= FrictionThreshold
// approvals of cls within the last FrictionWindow. Old entries are
// pruned as a side effect.
func (a *TTYApprover) shouldFriction(cls RiskClass) bool {
	if a.FrictionThreshold <= 0 || a.FrictionWindow <= 0 {
		return false
	}
	ttyApprovalMu.Lock()
	defer ttyApprovalMu.Unlock()
	return a.prunedApprovalCountLocked(cls) >= a.FrictionThreshold
}

// recentApprovalCount returns how many approvals of cls fall inside the
// current friction window (expired entries pruned as a side effect).
func (a *TTYApprover) recentApprovalCount(cls RiskClass) int {
	ttyApprovalMu.Lock()
	defer ttyApprovalMu.Unlock()
	return a.prunedApprovalCountLocked(cls)
}

// SetTrustedClasses atomically sets the trusted classes map.
// Takes ownership of the provided map — caller must not write to it after calling.
func (a *TTYApprover) SetTrustedClasses(m map[RiskClass]bool) {
	a.mu.Lock()
	a.TrustedClasses = m
	a.mu.Unlock()
}

// SetTrustAll enables or disables blanket trust for all risk classes.
// When enabled, PromptCommand returns nil for every call (used by batch approval).
func (a *TTYApprover) SetTrustAll(enabled bool) {
	a.mu.Lock()
	a.trustAll = enabled
	a.mu.Unlock()
}

func (a *TTYApprover) PromptCommand(cls RiskClass, cmd, description string) error {
	return a.prompt(cls, cmd, description)
}

func (a *TTYApprover) PromptOperation(op ToolOperation) error {
	return a.prompt(op.Risk, op.Resource, op.Name)
}

func (a *TTYApprover) prompt(cls RiskClass, cmd, description string) error {
	// Serialize all TTY prompts process-wide. Concurrent tool calls
	// otherwise open /dev/tty independently and race for keystrokes.
	ttyPromptMu.Lock()
	defer ttyPromptMu.Unlock()
	return a.promptLocked(cls, cmd, description)
}

// promptLocked is the inner prompt implementation. The caller must hold
// ttyPromptMu. It may recurse for the "context" command or after telling
// the user that trust-session is unavailable for a high-impact class.
func (a *TTYApprover) promptLocked(cls RiskClass, cmd, description string) error {
	// Check session trust cache
	a.mu.Lock()
	trusted := a.TrustedClasses != nil && a.TrustedClasses[cls]
	trusted = trusted || a.trustAll
	a.mu.Unlock()
	if trusted {
		return nil
	}

	// Open /dev/tty for interactive approval
	tty, err := os.OpenFile(a.TTYPath, os.O_RDWR, 0)
	if err != nil {
		// Non-interactive: use the configured fallback (H-7).
		if a.DangerousConfig != nil {
			switch a.DangerousConfig.NonInteractiveAction() {
			case Allow:
				return nil
			case ReadOnly:
				// Reads proceed, mutations do not. A read is either a
				// Safe-classified shell command (ls, cat — the classifier
				// already judged it non-mutating) or a native read tool
				// (description carries the tool name) targeting anything
				// below the system_write tier — sensitive-location reads
				// still gate.
				if Rank(cls) < Rank(SystemWrite) && (cls == Safe || isReadToolName(description)) {
					return nil
				}
				return fmt.Errorf("operation denied (non-interactive read_only mode): %s", cmd)
			default: // deny
				return fmt.Errorf("operation denied (non-interactive mode): %s", cmd)
			}
		}
		return nil
	}
	defer tty.Close()

	// Trust-class shortcut is disabled for the highest-impact classes.
	// Destructive and Blocked always require per-call approval to defeat
	// approval-fatigue attacks where the model batches a benign trust grant
	// with a dangerous payload. Unknown is included because it is the
	// fail-closed catch-all for unrecognised verbs — class-trusting it would
	// blanket-approve every future obfuscated/novel command. ToolBatchClass
	// aggregates tools of differing classes, so trusting it would
	// blanket-approve every later batch (see TrustShortcutAllowed).
	allowTrust := TrustShortcutAllowed(cls)

	// Approval-fatigue mitigation: if the user has already approved
	// this class FrictionThreshold times in FrictionWindow, the next
	// prompt requires the full word "approve" (no "a" shortcut) and we
	// pause briefly before accepting input. This breaks reflex
	// click-through, which is the primary failure mode under sustained
	// LLM-driven approval pressure.
	friction := a.shouldFriction(cls)

	// Build the prompt
	fmt.Fprintf(os.Stderr, "\n⚠️  \033[1mRisk:\033[0m  %s\n", cls)
	fmt.Fprintf(os.Stderr, "   \033[1mRun:\033[0m  %s\n", cmd)
	if description != "" {
		fmt.Fprintf(os.Stderr, "   \033[1mWhy:\033[0m  %s\n", description)
	}
	if friction {
		fmt.Fprintf(os.Stderr, "\n   ⚠️  You have approved %d %s operations in the last %s.\n",
			a.recentApprovalCount(cls), cls, a.FrictionWindow)
		fmt.Fprint(os.Stderr, "   Type 'approve' (full word) to proceed, anything else to deny: ")
		if a.pauseFn != nil {
			a.pauseFn(1500 * time.Millisecond)
		}
	} else if allowTrust {
		fmt.Fprint(os.Stderr, "\n   [A]pprove  [D]eny  [T]rust session: ")
	} else {
		fmt.Fprintf(os.Stderr, "\n   [A]pprove  [D]eny  (trust-session disabled for %s): ", cls)
	}

	// Read a single line of input from the TTY
	reader := bufio.NewReader(tty)
	line, err := reader.ReadString('\n')
	if err != nil {
		return fmt.Errorf("approval prompt error: %w", err)
	}
	line = strings.TrimSpace(strings.ToLower(line))

	// In friction mode, only the full word "approve" is accepted.
	if friction {
		if line == "approve" {
			a.recordApproval(cls)
			return nil
		}
		return fmt.Errorf("operation denied by user (friction mode): %s", cmd)
	}

	switch line {
	case "a", "approve":
		a.recordApproval(cls)
		return nil
	case "t", "trust":
		if !allowTrust {
			fmt.Fprintf(os.Stderr, "   trust-session not available for %s — type 'a' to approve once or 'd' to deny\n", cls)
			return a.promptLocked(cls, cmd, description)
		}
		// Cache this risk class for the session
		a.mu.Lock()
		if a.TrustedClasses != nil {
			a.TrustedClasses[cls] = true
		}
		a.mu.Unlock()
		return nil
	case "?", "context":
		fmt.Fprintf(tty, "\n  Command: %s\n", cmd)
		fmt.Fprintf(tty, "  Risk class: %s\n", cls)
		if description != "" {
			fmt.Fprintf(tty, "  Description: %s\n", description)
		}
		a.mu.Lock()
		trusted := a.TrustedClasses[cls]
		a.mu.Unlock()
		fmt.Fprintf(tty, "  Trust this class: %v\n", trusted)
		// Re-prompt
		return a.promptLocked(cls, cmd, description)
	default:
		return fmt.Errorf("operation denied by user: %s", cmd)
	}
}

// parseAction is kept as TTYApprover doesn't need it — it delegates to DangerousConfig.
