package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/BackendStack21/odek/internal/memory"
)

// memoryCmd handles `odek memory <list|promote> [args]`.
//
// This is the human-gated surface for the episode-memory trust control.
// Episodes whose originating session touched external content (web/http/MCP/
// audio, or reads of sensitive paths) are stored but excluded from recall
// until a human promotes them. Promotion lives HERE — on the CLI — and is
// deliberately NOT exposed as an agent tool, so a prompt-injected agent cannot
// approve its own poisoned memory.
func memoryCmd(args []string) error {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: odek memory <list|promote> [args]\n")
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

	default:
		return fmt.Errorf("unknown memory subcommand %q (expected: list, promote)", sub)
	}
}
