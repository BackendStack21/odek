package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/llm"
	"github.com/BackendStack21/odek/internal/memory"
	"github.com/BackendStack21/odek/internal/memory/extended"
)

// memoryCmd handles `odek memory <list|promote|extended> [args]`.
//
// This is the human-gated surface for the episode-memory trust control.
// Episodes whose originating session touched external content (web/http/MCP/
// audio, or reads of sensitive paths) are stored but excluded from recall
// until a human promotes them. Promotion lives HERE — on the CLI — and is
// deliberately NOT exposed as an agent tool, so a prompt-injected agent cannot
// approve its own poisoned memory.
func memoryCmd(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: odek memory <list|promote|extended> [args]\n")
		return nil
	}

	dir := expandHome("~/.odek/memory")
	store := memory.NewEpisodeStore(dir, nil)

	sub := args[0]
	subArgs := args[1:]

	switch sub {
	case "list", "ls", "pending":
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
			fmt.Printf("• %s  (%d turns, %s)\n", ep.SessionID, ep.Turns, ep.CreatedAt.Format("2006-01-02 15:04"))
			if len(ep.Provenance.Sources) > 0 {
				fmt.Printf("    sources: %s\n", strings.Join(ep.Provenance.Sources, ", "))
			}
			fmt.Printf("    %s\n\n", ep.Summary)
		}
		fmt.Println("Review the summary above, then promote with:  odek memory promote <session_id>")
		return nil

	case "promote":
		if len(subArgs) == 0 {
			return fmt.Errorf("usage: odek memory promote <session_id>")
		}
		id := subArgs[0]
		if err := store.Promote(id); err != nil {
			return err
		}
		fmt.Printf("odek: promoted episode %q — it can now be recalled into future sessions\n", id)
		return nil

	case "extended":
		return extendedMemoryCmd(dir, subArgs)

	default:
		return fmt.Errorf("unknown memory subcommand %q (expected: list, promote, extended)", sub)
	}
}

// extendedMemoryCmd handles `odek memory extended <subcommand>`.
func extendedMemoryCmd(dir string, args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: odek memory extended <forget|promote|pin|quarantine|compact|stats|consolidate|nudges|pending|confirm|reject> [args]\n")
		return nil
	}

	sub := args[0]
	subArgs := args[1:]

	extDir := filepath.Join(dir, "extended")
	cfg := extended.DefaultConfig()
	enabled := true
	cfg.Enabled = &enabled
	em := extended.New(extDir, nil, cfg)

	switch sub {
	case "forget":
		if len(subArgs) == 0 {
			return fmt.Errorf("usage: odek memory extended forget <atom_id>")
		}
		id := subArgs[0]
		if err := em.ForgetAtom(id); err != nil {
			return err
		}
		fmt.Printf("odek: forgot atom %q\n", id)
		return nil

	case "promote":
		if len(subArgs) == 0 {
			return fmt.Errorf("usage: odek memory extended promote <atom_id>")
		}
		id := subArgs[0]
		if err := em.PromoteAtom(id); err != nil {
			return err
		}
		fmt.Printf("odek: promoted atom %q — it can now be recalled into future sessions\n", id)
		return nil

	case "pin":
		if len(subArgs) == 0 {
			return fmt.Errorf("usage: odek memory extended pin <atom_id>")
		}
		id := subArgs[0]
		if err := em.PinAtom(id); err != nil {
			return err
		}
		fmt.Printf("odek: pinned atom %q\n", id)
		return nil

	case "quarantine":
		entries, err := em.ListQuarantineEntries()
		if err != nil {
			return err
		}
		if len(entries) == 0 {
			fmt.Println("No atoms in quarantine.")
			return nil
		}
		fmt.Printf("%d atom(s) in quarantine (excluded from recall):\n\n", len(entries))
		for _, e := range entries {
			fmt.Printf("• %s [%s] %s\n", e.ID, e.SourceClass, truncate(e.Text, 120))
			if e.Reason != "" {
				fmt.Printf("  reason: %s\n", truncate(e.Reason, 120))
			}
		}
		fmt.Println("\nReview and restore with: odek memory extended promote <atom_id>")
		return nil

	case "compact":
		em.Compact()
		fmt.Println("odek: Extended Memory vector index compaction triggered in the background")
		return nil

	case "stats":
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
		fmt.Printf("  index vectors:     %d (dirty: %v)\n", st.IndexVectors, st.IndexDirty)
		fmt.Printf("  store size:        %.1f MiB\n", float64(st.StoreSizeBytes)/(1<<20))
		fmt.Printf("  recall timeouts:   %d\n", st.RecallTimeouts)
		fmt.Printf("  recall failures:   %d\n", st.RecallFailures)
		if st.RecallTimeouts+st.RecallFailures > 0 {
			fmt.Println("  warning: recall degraded this process — check LLM/embedding backend latency")
		}
		return nil

	case "consolidate":
		// Merging near-duplicate atoms needs an LLM; resolve the operator
		// backend the same way the agent does.
		resolved := config.LoadConfig(config.CLIFlags{})
		if resolved.APIKey == "" {
			return fmt.Errorf("memory extended consolidate requires an LLM backend (no API key resolved)")
		}
		llmClient := llm.New(resolved.BaseURL, resolved.APIKey, resolved.Model, "", 0, 120*time.Second)
		emLLM := extended.New(extDir, llmClient, cfg)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		merged, err := emLLM.ConsolidateAtoms(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("odek: consolidation complete — %d atom(s) merged into existing or new entries\n", merged)
		return nil

	case "nudges":
		// Generating nudges needs an LLM; resolve the operator backend the
		// same way the agent does (same pattern as consolidate).
		resolved := config.LoadConfig(config.CLIFlags{})
		if resolved.APIKey == "" {
			return fmt.Errorf("memory extended nudges requires an LLM backend (no API key resolved)")
		}
		llmClient := llm.New(resolved.BaseURL, resolved.APIKey, resolved.Model, "", 0, 120*time.Second)
		emLLM := extended.New(extDir, llmClient, cfg)
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
			fmt.Printf("• [%s] %s\n", n.Kind, n.Text)
			if len(n.SourceAtomIDs) > 0 {
				fmt.Printf("    atoms: %s\n", strings.Join(n.SourceAtomIDs, ", "))
			}
		}
		return nil

	case "pending":
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
			fmt.Printf("• %s | %s = %q (confidence %.2f)\n", p.ID, p.Field, truncate(p.Value, 120), p.Confidence)
			if p.Evidence != "" {
				fmt.Printf("  evidence: %s\n", truncate(p.Evidence, 120))
			}
		}
		fmt.Println("\nConfirm with: odek memory extended confirm <id>")
		return nil

	case "confirm":
		if len(subArgs) == 0 {
			return fmt.Errorf("usage: odek memory extended confirm <pending_id>")
		}
		id := subArgs[0]
		if err := em.ConfirmPendingReview(id); err != nil {
			return err
		}
		fmt.Printf("odek: confirmed pending review %q\n", id)
		return nil

	case "reject":
		if len(subArgs) == 0 {
			return fmt.Errorf("usage: odek memory extended reject <pending_id>")
		}
		id := subArgs[0]
		if err := em.RejectPendingReview(id); err != nil {
			return err
		}
		fmt.Printf("odek: rejected pending review %q\n", id)
		return nil

	default:
		return fmt.Errorf("unknown extended memory subcommand %q (expected: forget, promote, pin, quarantine, compact, stats, consolidate, nudges, pending, confirm, reject)", sub)
	}
}
