package danger

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"
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
	approvalLog       map[RiskClass][]time.Time
	// pauseFn is overridden in tests so we don't actually sleep.
	pauseFn func(d time.Duration)
}

// NewTTYApprover creates a TTYApprover with the given config.
func NewTTYApprover(cfg *DangerousConfig) *TTYApprover {
	return &TTYApprover{
		DangerousConfig:   cfg,
		TrustedClasses:    make(map[RiskClass]bool),
		TTYPath:           "/dev/tty",
		FrictionThreshold: 3,
		FrictionWindow:    60 * time.Second,
		approvalLog:       make(map[RiskClass][]time.Time),
		pauseFn:           func(d time.Duration) { time.Sleep(d) },
	}
}

// recordApproval logs an approval timestamp for the given class and
// returns true if the next prompt for this class should engage the
// high-friction path.
func (a *TTYApprover) recordApproval(cls RiskClass) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.approvalLog == nil {
		a.approvalLog = make(map[RiskClass][]time.Time)
	}
	a.approvalLog[cls] = append(a.approvalLog[cls], time.Now())
}

// shouldFriction returns true when there have been >= FrictionThreshold
// approvals of cls within the last FrictionWindow. Old entries are
// pruned as a side effect.
func (a *TTYApprover) shouldFriction(cls RiskClass) bool {
	if a.FrictionThreshold <= 0 || a.FrictionWindow <= 0 {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	cutoff := time.Now().Add(-a.FrictionWindow)
	log := a.approvalLog[cls]
	kept := log[:0]
	for _, t := range log {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	a.approvalLog[cls] = kept
	return len(kept) >= a.FrictionThreshold
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
		// Non-interactive: use configured fallback
		if a.DangerousConfig != nil && a.DangerousConfig.NonInteractiveAction() == Deny {
			return fmt.Errorf("operation denied (non-interactive mode): %s", cmd)
		}
		return nil
	}
	defer tty.Close()

	// Trust-class shortcut is disabled for the two highest-impact
	// classes. Destructive and Blocked operations always require a
	// per-call approval to defeat approval-fatigue attacks where the
	// model batches a benign destructive-class trust grant with a
	// destructive payload.
	allowTrust := cls != Destructive && cls != Blocked

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
			a.FrictionThreshold, cls, a.FrictionWindow)
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
			return a.prompt(cls, cmd, description)
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
		return a.prompt(cls, cmd, description)
	default:
		return fmt.Errorf("operation denied by user: %s", cmd)
	}
}

// parseAction is kept as TTYApprover doesn't need it — it delegates to DangerousConfig.
