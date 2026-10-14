package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/guard"
	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/memory/approval"
	"github.com/BackendStack21/odek/internal/memory/extended"
)

const memoryUsage = `Usage: odek memory <subcommand> [args]

Episodes (session summaries; untrusted ones wait for human review):
  list [--all]                 Pending episodes; --all shows every episode with its state
  promote <session-id>...      Approve pending episodes for recall; with one id,
                               --sha256 <hash> (from list) refuses a changed summary
  discard <session-id>...      Delete pending episodes (alias: reject)

Facts (always in the system prompt):
  facts [user|env]             Show the fact files with usage/cap
  facts add <user|env> <text>
  facts replace <user|env> <old-substring> <text>
  facts remove <user|env> <old-substring>

Extended Memory (atoms):
  extended <subcommand>        See: odek memory extended help
`

const extendedUsage = `Usage: odek memory extended <subcommand> [args]

  stats                        Store, quarantine and persisted-index summary
  compact                      Rebuild and persist the vector index (waits)
  prune [--dry-run]            Delete TTL-expired ephemeral atoms, orphan chunks,
                               expired quarantine entries
  triage [--quarantine] <re>   List live atoms matching a Go regexp; with
                               --quarantine move them to quarantine for review
                               (use -- before a pattern that starts with --)
  quarantine                   List quarantined atoms
  promote <atom-id>            Restore a quarantined atom as user_approved
  forget <atom-id>             Delete an atom (live or quarantined)
  pin <atom-id>                Pin a live atom (never evicted)
  pending                      List pending user-model inferences
  confirm <pending-id>         Apply a pending inference
  reject <pending-id>          Drop a pending inference
  consolidate                  LLM-merge near-duplicate atoms
  nudges                       Preview proactive nudges
`

func isHelpArg(s string) bool {
	return s == "help" || s == "--help" || s == "-h"
}

// exactArgs rejects stray arguments: every subcommand takes a fixed shape,
// and ignoring extras hid typos such as `promote a b c`.
func exactArgs(cmd string, args []string, n int) error {
	if len(args) != n {
		return fmt.Errorf("usage: odek memory %s (takes %d argument(s), got %d)", cmd, n, len(args))
	}
	return nil
}

// memoryCmd handles `odek memory <list|promote|discard|facts|extended> [args]`.
//
// This is the human-gated surface for the episode-memory trust control.
// Episodes whose originating session touched external content (web/http/MCP/
// audio, or reads of sensitive paths) are stored but excluded from recall
// until a human promotes them. Promotion lives HERE — on the CLI — and is
// deliberately NOT exposed as an agent tool, so a prompt-injected agent cannot
// approve its own poisoned memory.
func memoryCmd(args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, memoryUsage)
		return nil
	}

	dir := expandHome("~/.odek/memory")
	store := memory.NewEpisodeStore(dir, nil)

	sub := args[0]
	subArgs := args[1:]

	switch {
	case isHelpArg(sub):
		fmt.Print(memoryUsage)
		return nil

	case sub == "list" || sub == "ls" || sub == "pending":
		all := false
		for _, a := range subArgs {
			if a == "--all" {
				all = true
				continue
			}
			if isHelpArg(a) {
				fmt.Print(memoryUsage)
				return nil
			}
			return fmt.Errorf("usage: odek memory list [--all] (unexpected argument %q)", danger.SanitizeInline(a))
		}
		if all {
			return memoryListAll(store)
		}
		return memoryListPending(store)

	case sub == "promote":
		ids, wantHash, err := parsePromoteArgs(subArgs)
		if err != nil {
			return err
		}
		var errs []error
		for _, id := range ids {
			// Without an explicit reviewed hash, bind the approval to the
			// exact text this command is about to show, so what is printed
			// is what becomes recallable even if the file changes meanwhile.
			hash := wantHash
			if hash == "" {
				if full, rerr := store.Read(id); rerr == nil {
					hash = episodeSummaryHash(full)
				}
			}
			promoted, err := store.PromoteIfHash(id, hash)
			if errors.Is(err, memory.ErrEpisodeSummaryChanged) {
				errs = append(errs, fmt.Errorf("episode %q changed since it was reviewed; run odek memory list and review it again", danger.SanitizeInline(id)))
				continue
			}
			if err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Printf("odek: promoted episode %q — it can now be recalled into future sessions:\n\n    %s\n",
				danger.SanitizeInline(id), strings.ReplaceAll(danger.SanitizeForDisplay(strings.TrimSpace(promoted)), "\n", "\n    "))
		}
		return errors.Join(errs...)

	case sub == "discard" || sub == "reject":
		if len(subArgs) == 0 {
			return fmt.Errorf("usage: odek memory discard <session_id> [<session_id> ...]")
		}
		var errs []error
		for _, id := range subArgs {
			if err := store.Discard(id); err != nil {
				errs = append(errs, err)
				continue
			}
			fmt.Printf("odek: discarded episode %q — it was removed from memory and will never be recalled\n", danger.SanitizeInline(id))
		}
		return errors.Join(errs...)

	case sub == "facts":
		return memoryFactsCmd(dir, subArgs)

	case sub == "extended":
		return extendedMemoryCmd(dir, subArgs)

	default:
		return fmt.Errorf("unknown memory subcommand %q (expected: list, promote, discard, facts, extended; see odek memory help)", danger.SanitizeInline(sub))
	}
}

// parsePromoteArgs splits `promote` arguments into session ids and the
// optional --sha256 reviewed-text hash (only valid with a single id).
func parsePromoteArgs(args []string) ([]string, string, error) {
	var ids []string
	hash := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--sha256" {
			if i+1 >= len(args) || args[i+1] == "" {
				return nil, "", fmt.Errorf("usage: odek memory promote <session_id> [--sha256 <hash>] (--sha256 needs a value)")
			}
			hash = args[i+1]
			i++
			continue
		}
		ids = append(ids, args[i])
	}
	if len(ids) == 0 {
		return nil, "", fmt.Errorf("usage: odek memory promote <session_id> [<session_id> ...] [--sha256 <hash>]")
	}
	if hash != "" && len(ids) != 1 {
		return nil, "", fmt.Errorf("usage: odek memory promote: --sha256 applies to exactly one session id")
	}
	return ids, hash, nil
}

// memoryListPending prints the episodes waiting for human review with their
// full stored text — what recall would replay — escaped for the terminal.
func memoryListPending(store *memory.EpisodeStore) error {
	pending, err := store.PendingReview()
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		fmt.Println("No episodes pending review — all stored episodes are recallable.")
		return nil
	}
	fmt.Printf("%d episode(s) pending review (excluded from recall until promoted):\n\n", len(pending))
	for _, ep := range pending {
		fmt.Printf("• %s  (%d turns, %s)\n", danger.SanitizeInline(ep.SessionID), ep.Turns, ep.CreatedAt.Format("2006-01-02 15:04"))
		if len(ep.Provenance.Sources) > 0 {
			fmt.Printf("    sources: %s\n", danger.SanitizeInline(strings.Join(ep.Provenance.Sources, ", ")))
		}
		text := ep.Summary
		if full, err := store.Read(ep.SessionID); err == nil {
			text = full
		}
		fmt.Printf("    sha256: %s\n", episodeSummaryHash(text))
		fmt.Printf("    %s\n\n", strings.ReplaceAll(danger.SanitizeForDisplay(strings.TrimSpace(text)), "\n", "\n    "))
	}
	fmt.Println("Review the summary above, then promote with:  odek memory promote <session_id> [--sha256 <hash>]")
	fmt.Println("                              or drop with:  odek memory discard <session_id>...")
	return nil
}

// episodeState names an episode's trust state as the review gate sees it.
func episodeState(p memory.EpisodeProvenance) string {
	switch {
	case !p.Untrusted:
		return "trusted"
	case p.UserApproved:
		return "approved"
	case p.AutoApproved:
		return "auto-approved"
	default:
		return "pending"
	}
}

// memoryListAll prints every stored episode, newest first, with its state
// and the index preview.
func memoryListAll(store *memory.EpisodeStore) error {
	idx, err := store.ReadIndex()
	if err != nil {
		return err
	}
	if len(idx) == 0 {
		fmt.Println("No episodes stored.")
		return nil
	}
	counts := map[string]int{}
	for _, ep := range idx {
		counts[episodeState(ep.Provenance)]++
	}
	fmt.Printf("%d episode(s): %d trusted, %d approved, %d auto-approved, %d pending\n\n",
		len(idx), counts["trusted"], counts["approved"], counts["auto-approved"], counts["pending"])
	for _, ep := range idx {
		fmt.Printf("• %s  [%s]  %d turns, %s\n", danger.SanitizeInline(ep.SessionID), episodeState(ep.Provenance), ep.Turns, ep.CreatedAt.Format("2006-01-02 15:04"))
		if len(ep.Provenance.Sources) > 0 {
			fmt.Printf("    sources: %s\n", danger.SanitizeInline(strings.Join(ep.Provenance.Sources, ", ")))
		}
		fmt.Printf("    %s\n", danger.SanitizeInline(strings.TrimSpace(ep.Summary)))
	}
	return nil
}

// memoryFactsCmd handles `odek memory facts [...]`: the operator's direct
// view of and editor for the fact files that every system prompt carries.
// Writes go through the memory tool's filters — the 2048-byte field bound,
// the injection scan (with the configured guard sidecar when one is set),
// credential redaction and the pipe-to-shell filter; merge-on-write is off
// because an operator edit is explicit.
func memoryFactsCmd(dir string, args []string) error {
	resolved := config.LoadConfig(config.CLIFlags{})
	cfg := resolved.Memory
	cfg.Enabled = memory.BoolPtr(true)
	cfg.MergeOnWrite = memory.BoolPtr(false)
	cfg.ConsolidateAtCapPct = new(int) // operator edits never trigger LLM consolidation
	mm := memory.NewMemoryManager(dir, nil, cfg)
	if g, err := guard.New(&resolved.Guard); err == nil {
		mm.SetGuard(g, resolved.Guard)
	} else {
		fmt.Fprintf(os.Stderr, "odek: warning: guard unavailable, local scan only: %v\n", err)
	}
	bounds := func(action, target, content, oldText string) error {
		return approval.CheckBounds(approval.Args{Action: action, Target: target, Content: content, OldText: oldText})
	}

	if len(args) == 0 {
		return memoryFactsShow(mm, "user", "env")
	}
	switch args[0] {
	case "user", "env":
		if err := exactArgs("facts <user|env>", args, 1); err != nil {
			return err
		}
		return memoryFactsShow(mm, args[0])
	case "show":
		if err := exactArgs("facts show", args, 1); err != nil {
			return err
		}
		return memoryFactsShow(mm, "user", "env")
	case "add":
		if err := exactArgs("facts add <user|env> <text>", args, 3); err != nil {
			return err
		}
		if err := checkFactTarget(args[1]); err != nil {
			return err
		}
		if err := bounds("add", args[1], args[2], ""); err != nil {
			return err
		}
		if err := mm.AddFact(args[1], args[2]); err != nil {
			return err
		}
		fmt.Printf("odek: added to memory[%s]\n", args[1])
		return memoryFactsShow(mm, args[1])
	case "replace":
		if err := exactArgs("facts replace <user|env> <old-substring> <text>", args, 4); err != nil {
			return err
		}
		if err := checkFactTarget(args[1]); err != nil {
			return err
		}
		if err := bounds("replace", args[1], args[3], args[2]); err != nil {
			return err
		}
		if err := mm.ReplaceFact(args[1], args[2], args[3]); err != nil {
			return err
		}
		fmt.Printf("odek: replaced entry in memory[%s]\n", args[1])
		return memoryFactsShow(mm, args[1])
	case "remove":
		if err := exactArgs("facts remove <user|env> <old-substring>", args, 3); err != nil {
			return err
		}
		if err := checkFactTarget(args[1]); err != nil {
			return err
		}
		if err := bounds("remove", args[1], "", args[2]); err != nil {
			return err
		}
		if err := mm.RemoveFact(args[1], args[2]); err != nil {
			return err
		}
		fmt.Printf("odek: removed entry from memory[%s]\n", args[1])
		return memoryFactsShow(mm, args[1])
	default:
		if isHelpArg(args[0]) {
			fmt.Print(memoryUsage)
			return nil
		}
		return fmt.Errorf("unknown facts subcommand %q (expected: user, env, add, replace, remove)", danger.SanitizeInline(args[0]))
	}
}

func checkFactTarget(target string) error {
	if target != "user" && target != "env" {
		return fmt.Errorf("fact target must be user or env, got %q", danger.SanitizeInline(target))
	}
	return nil
}

// memoryFactsShow prints each target's entries numbered, with bytes used
// against the cap — the same figures the agent's stats action sees.
func memoryFactsShow(mm *memory.MemoryManager, targets ...string) error {
	for i, target := range targets {
		entries, used, capBytes, err := mm.FactStats(target)
		if err != nil {
			return err
		}
		if i > 0 {
			fmt.Println()
		}
		pct := 0
		if capBytes > 0 {
			pct = used * 100 / capBytes
		}
		fmt.Printf("── memory[%s]  %d/%d bytes (%d%%)  %d entr%s ──\n", target, used, capBytes, pct, len(entries), plural(len(entries), "y", "ies"))
		if len(entries) == 0 {
			fmt.Println("(empty)")
			continue
		}
		for n, e := range entries {
			fmt.Printf("[%d] %s\n", n+1, danger.SanitizeForDisplay(e))
		}
	}
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// extendedMemoryCmd handles `odek memory extended <subcommand>`.
func extendedMemoryCmd(dir string, args []string) error {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, extendedUsage)
		return nil
	}

	sub := args[0]
	subArgs := args[1:]
	if isHelpArg(sub) {
		fmt.Print(extendedUsage)
		return nil
	}
	switch sub {
	case "forget", "promote", "pin", "quarantine", "compact", "stats", "prune", "triage", "consolidate", "nudges", "pending", "confirm", "reject":
	default:
		// Judged before the store is opened so a typo creates no files.
		return fmt.Errorf("unknown extended memory subcommand %q (expected: stats, compact, prune, triage, quarantine, promote, forget, pin, pending, confirm, reject, consolidate, nudges; see odek memory extended help)", danger.SanitizeInline(sub))
	}

	// Open the store with the operator's resolved Extended Memory config —
	// TTLs and the embedding backend must match what the agent's recall
	// uses, or prune deletes what recall still serves and compact rebuilds
	// the index in the wrong space. No eviction on open: reporting commands
	// change nothing, prune evicts explicitly.
	extDir := filepath.Join(dir, "extended")
	resolved := config.LoadConfig(config.CLIFlags{})
	cfg := memory.ExtendedConfigFor(resolved.Memory)
	cfg.Enabled = memory.BoolPtr(true)
	em := extended.NewForMaintenance(extDir, nil, cfg)
	defer em.Close()

	switch sub {
	case "forget":
		if err := exactArgs("extended forget <atom_id>", subArgs, 1); err != nil {
			return err
		}
		id := subArgs[0]
		if err := em.ForgetAtom(id); err != nil {
			return err
		}
		fmt.Printf("odek: forgot atom %q\n", danger.SanitizeInline(id))
		return nil

	case "promote":
		if err := exactArgs("extended promote <atom_id>", subArgs, 1); err != nil {
			return err
		}
		id := subArgs[0]
		if err := em.PromoteAtom(id); err != nil {
			return err
		}
		fmt.Printf("odek: promoted atom %q — it can now be recalled into future sessions\n", danger.SanitizeInline(id))
		return nil

	case "pin":
		if err := exactArgs("extended pin <atom_id>", subArgs, 1); err != nil {
			return err
		}
		id := subArgs[0]
		if err := em.PinAtom(id); err != nil {
			return err
		}
		fmt.Printf("odek: pinned atom %q\n", danger.SanitizeInline(id))
		return nil

	case "quarantine":
		if err := exactArgs("extended quarantine", subArgs, 0); err != nil {
			return err
		}
		entries, err := em.ListQuarantineEntries()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("No atoms in quarantine.")
			return nil
		}
		fmt.Printf("%d atom(s) in quarantine (excluded from recall):\n\n", len(entries))
		// This listing is the review step before promote and the atoms
		// come from web/MCP/tool output: show the whole text (what recall
		// would replay) with control, bidi and invisible characters escaped,
		// so an escape sequence cannot erase or overwrite what is shown.
		for _, e := range entries {
			fmt.Printf("• %s [%s] %s\n", danger.SanitizeInline(e.ID), danger.SanitizeInline(e.SourceClass), danger.SanitizeInline(e.Text))
			if e.Reason != "" {
				fmt.Printf("  reason: %s\n", danger.SanitizeInline(truncate(e.Reason, 120)))
			}
		}
		fmt.Println("\nReview and restore with: odek memory extended promote <atom_id>")
		return nil

	case "compact":
		if err := exactArgs("extended compact", subArgs, 0); err != nil {
			return err
		}
		// Compact runs in the background; a CLI process must wait for it
		// or the rebuild dies with the process and nothing is persisted.
		em.Compact()
		if err := em.Close(); err != nil {
			return err
		}
		vectors, stale, ok := em.PersistedIndex()
		if !ok {
			return fmt.Errorf("extended memory: vector index was not persisted (see the log above)")
		}
		state := "current"
		if stale {
			state = "stale"
		}
		fmt.Printf("odek: vector index rebuilt — %d vectors persisted (%s)\n", vectors, state)
		return nil

	case "stats":
		if err := exactArgs("extended stats", subArgs, 0); err != nil {
			return err
		}
		st := em.Stats()
		fmt.Println("Extended Memory stats:")
		fmt.Printf("  live atoms:        %d\n", st.LiveAtoms)
		fmt.Printf("  quarantined atoms: %d\n", st.QuarantinedAtoms)
		if len(st.QuarantineReasons) > 0 {
			reasons := make([]string, 0, len(st.QuarantineReasons))
			for r := range st.QuarantineReasons {
				reasons = append(reasons, r)
			}
			sort.Strings(reasons)
			for _, r := range reasons {
				fmt.Printf("    %-16s %d\n", r+":", st.QuarantineReasons[r])
			}
		}
		// The in-process index is never built by a CLI invocation; report
		// what the last long-lived process left on disk instead. Recall
		// timeout/failure counters are process-local too, so the CLI does
		// not print them (serve exposes them via the API).
		switch vectors, stale, ok := em.PersistedIndex(); {
		case !ok:
			fmt.Println("  index vectors:     none persisted (run: odek memory extended compact)")
		case stale:
			fmt.Printf("  index vectors:     %d (persisted, stale — run: odek memory extended compact)\n", vectors)
		default:
			fmt.Printf("  index vectors:     %d (persisted, current)\n", vectors)
		}
		fmt.Printf("  store size:        %.1f MiB\n", float64(st.StoreSizeBytes)/(1<<20))
		return nil

	case "prune":
		dryRun := false
		for _, a := range subArgs {
			if a == "--dry-run" {
				dryRun = true
				continue
			}
			return fmt.Errorf("usage: odek memory extended prune [--dry-run] (unexpected argument %q)", danger.SanitizeInline(a))
		}
		rep, err := em.Prune(time.Now().UTC(), dryRun)
		if err != nil {
			return err
		}
		mode := "prune"
		if dryRun {
			mode = "prune (dry run, nothing changed)"
		}
		fmt.Printf("odek: %s — %d expired ephemeral atom(s), %d orphan chunk file(s), %d expired quarantine entr%s\n",
			mode, rep.ExpiredAtoms, rep.OrphanChunks, rep.ExpiredQuarantine, plural(rep.ExpiredQuarantine, "y", "ies"))
		return nil

	case "triage":
		move := false
		var pattern string
		flagsDone := false
		for _, a := range subArgs {
			switch {
			case !flagsDone && a == "--":
				flagsDone = true // what follows is the pattern, even if it starts with --
			case !flagsDone && a == "--quarantine":
				move = true
			case pattern == "" && (flagsDone || !strings.HasPrefix(a, "--")):
				pattern = a
			default:
				return fmt.Errorf("usage: odek memory extended triage [--quarantine] [--] <regexp> (unexpected argument %q)", danger.SanitizeInline(a))
			}
		}
		if pattern == "" {
			return fmt.Errorf("usage: odek memory extended triage [--quarantine] <regexp>")
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("triage: invalid pattern: %w", err)
		}
		reason := "operator triage: " + pattern
		matched, err := em.Triage(re, move, reason)
		if len(matched) == 0 && err == nil {
			fmt.Println("No live atoms match.")
			return nil
		}
		fmt.Printf("%d live atom(s) match %q:\n\n", len(matched), danger.SanitizeInline(pattern))
		for _, a := range matched {
			pin := ""
			if a.Pin {
				pin = " (pinned, not moved)"
			}
			fmt.Printf("• %s [%s] %s%s\n", danger.SanitizeInline(a.ID), danger.SanitizeInline(a.Type), danger.SanitizeInline(a.Text), pin)
		}
		if err != nil {
			return err
		}
		if move {
			moved := 0
			for _, a := range matched {
				if !a.Pin {
					moved++
				}
			}
			fmt.Printf("\nodek: quarantined %d atom(s) (reason: %s) — review with: odek memory extended quarantine\n", moved, danger.SanitizeInline(reason))
		} else {
			fmt.Println("\nMove them to quarantine for review with: odek memory extended triage --quarantine <regexp>")
		}
		return nil

	case "consolidate":
		if err := exactArgs("extended consolidate", subArgs, 0); err != nil {
			return err
		}
		// Merging near-duplicate atoms needs an LLM; the operator backend
		// is resolved the same way the agent does (memory.extended.llm overrides
		// the main model).
		if resolved.APIKey == "" {
			return fmt.Errorf("memory extended consolidate requires an LLM backend (no API key resolved)")
		}
		llmClient, err := llmclient.Dial(resolved.Provider, resolved.Model, resolved.APIKey, resolved.BaseURL)
		if err != nil {
			return err
		}
		emLLM := extended.New(extDir, extended.ResolveLLM(cfg, llmClient, resolved.Thinking), cfg)
		defer emLLM.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		merged, err := emLLM.ConsolidateAtoms(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("odek: consolidation complete — %d atom(s) merged into existing or new entries\n", merged)
		return nil

	case "nudges":
		if err := exactArgs("extended nudges", subArgs, 0); err != nil {
			return err
		}
		// Generating nudges needs an LLM (same pattern as consolidate).
		if resolved.APIKey == "" {
			return fmt.Errorf("memory extended nudges requires an LLM backend (no API key resolved)")
		}
		llmClient, err := llmclient.Dial(resolved.Provider, resolved.Model, resolved.APIKey, resolved.BaseURL)
		if err != nil {
			return err
		}
		emLLM := extended.New(extDir, extended.ResolveLLM(cfg, llmClient, resolved.Thinking), cfg)
		defer emLLM.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		nudges, err := emLLM.ProactiveNudges(ctx, 2)
		if err != nil {
			return err
		}
		if len(nudges) == 0 {
			fmt.Println("No nudges right now.")
			return nil
		}
		fmt.Println("Proactive nudges (preview — does not consume the daily cap):")
		for _, n := range nudges {
			fmt.Printf("• [%s] %s\n", danger.SanitizeInline(n.Kind), danger.SanitizeInline(n.Text))
			if len(n.SourceAtomIDs) > 0 {
				ids := make([]string, len(n.SourceAtomIDs))
				for i, id := range n.SourceAtomIDs {
					ids[i] = danger.SanitizeInline(id)
				}
				fmt.Printf("    atoms: %s\n", strings.Join(ids, ", "))
			}
		}
		return nil

	case "pending":
		if err := exactArgs("extended pending", subArgs, 0); err != nil {
			return err
		}
		pending, err := em.ListPendingReview()
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			fmt.Println("No pending user-model reviews.")
			return nil
		}
		fmt.Printf("%d pending review(s):\n\n", len(pending))
		for _, p := range pending {
			fmt.Printf("• %s | %s = %q (confidence %.2f)\n", danger.SanitizeInline(p.ID), danger.SanitizeInline(p.Field), truncate(p.Value, 120), p.Confidence)
			if p.Evidence != "" {
				fmt.Printf("  evidence: %s\n", danger.SanitizeInline(truncate(p.Evidence, 120)))
			}
		}
		fmt.Println("\nConfirm with: odek memory extended confirm <id>")
		return nil

	case "confirm":
		if err := exactArgs("extended confirm <pending_id>", subArgs, 1); err != nil {
			return err
		}
		id := subArgs[0]
		if err := em.ConfirmPendingReview(id); err != nil {
			return err
		}
		fmt.Printf("odek: confirmed pending review %q\n", danger.SanitizeInline(id))
		return nil

	case "reject":
		if err := exactArgs("extended reject <pending_id>", subArgs, 1); err != nil {
			return err
		}
		id := subArgs[0]
		if err := em.RejectPendingReview(id); err != nil {
			return err
		}
		fmt.Printf("odek: rejected pending review %q\n", danger.SanitizeInline(id))
		return nil

	default:
		return fmt.Errorf("unhandled extended memory subcommand %q", danger.SanitizeInline(sub))
	}
}
