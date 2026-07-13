package skills

import (
	"fmt"
	"io"
	"time"

	"github.com/BackendStack21/odek/internal/guard"
)

// AnalyzeMessages runs heuristic + LLM skill extraction over a conversation
// and returns the resulting suggestions, with provenance attached and
// (unless suppressSuggested is true) "suggested" notifier events fired.
//
// This is the half of the learn loop that does not require user
// interaction. The caller is expected to feed the result into either
// RunAutoSaveLoop (non-interactive, gated by config) or its own UI.
//
// Splitting it out of cmd/odek keeps the message-loop wiring small and
// lets skill-learning behaviour evolve in one place, covered by unit
// tests, instead of leaking through the main package.
func AnalyzeMessages(messages []LlmMessage, userMessages []string, sm *SkillManager, llmClient LLMClient, llmLearn, suppressSuggested bool) []SkillSuggestion {
	suggestions := RunAllHeuristics(messages, userMessages)

	// Tag every suggestion with the trust signals of the originating
	// session. Untrusted suggestions (browser/http_batch/read_file/MCP
	// reached) save with NeedsReview=true and never auto-load.
	prov := DeriveProvenance(messages)
	for i := range suggestions {
		suggestions[i].Provenance = prov
	}

	// Conversation-level extraction — uses full context, not just tool
	// patterns. Catches architectural decisions, debugging strategies, and
	// workflow patterns that the pattern-based heuristics miss.
	if llmLearn && llmClient != nil {
		if convSkill := ExtractSkillsFromConversation(llmClient, messages, userMessages); convSkill != nil {
			calls := ExtractToolCalls(messages)
			cmds := make([]string, 0, len(calls))
			for _, c := range calls {
				cmds = append(cmds, c.Input)
			}
			convSkill.CommandLog = cmds
			suggestions = append(suggestions, *convSkill)
		}
	}

	// Apply LLM enhancement to each suggestion.
	if llmLearn && llmClient != nil {
		calls := ExtractToolCalls(messages)
		for i := range suggestions {
			if enhanced := GenerateSkillWithLLM(llmClient, calls, userMessages, suggestions[i].Heuristic); enhanced != nil {
				enhanced.CommandLog = suggestions[i].CommandLog
				enhanced.Heuristic = suggestions[i].Heuristic
				suggestions[i] = *enhanced
			}
		}
	}

	if !suppressSuggested && sm != nil {
		for _, s := range suggestions {
			sm.Notifier.Notify(SkillEvent{
				Type:      "suggested",
				SkillName: s.Name,
				Heuristic: s.Heuristic,
				Body:      s.Body,
				Timestamp: time.Now().UTC(),
			})
		}
	}

	return suggestions
}

// RunAutoSaveLoop drives the non-interactive auto-save pipeline:
// filter against the skip list, save eligible suggestions, fire notifier
// events, and trigger micro-curation on any newly saved drafts.
//
// Returns true when auto-save was *attempted* (regardless of whether any
// individual save succeeded). A true return signals the caller to skip
// interactive prompting — the user has already opted into automation.
// Returns false when auto-save is disabled or gated by RequireLLM, in
// which case the caller should fall back to its own UI.
//
// verbose, when non-nil, receives human-readable progress lines. Pass
// nil (or io.Discard) for silent operation; the notifier events still
// fire either way so the WebUI/Telegram surfaces always see saves.
func RunAutoSaveLoop(filtered []SkillSuggestion, userDir string, sm *SkillManager, llmClient LLMClient, cfg SkillsConfig, g guard.Guard, guardCfg guard.Config, verbose io.Writer) bool {
	if !cfg.AutoSave.Enabled {
		return false
	}
	if cfg.AutoSave.RequireLLM && !cfg.LLMLearn {
		return false
	}

	result := AutoSaveSuggestions(filtered, userDir, cfg, g, guardCfg, false)

	if verbose != nil {
		for _, name := range result.Saved {
			if heuristic := result.Heuristics[name]; heuristic != "" {
				fmt.Fprintf(verbose, "   ✓ Auto-saved skill %q (%s)\n", name, heuristic)
			} else {
				fmt.Fprintf(verbose, "   ✓ Auto-saved skill %q\n", name)
			}
		}
		if result.Skipped > 0 {
			fmt.Fprintf(verbose, "   (%d previously skipped, suppressed)\n", result.Skipped)
		}
		for _, name := range result.Declined {
			fmt.Fprintf(verbose, "   ⚠ Declined to auto-save tainted skill %q (review with --force to save)\n", name)
		}
		for _, name := range result.GuardFlagged {
			fmt.Fprintf(verbose, "   ⚠ Guard flagged skill %q (saved but pinned to manual review)\n", name)
		}
		for _, name := range result.Failed {
			fmt.Fprintf(verbose, "   ⚠ Quality gate failed for %q (use --no-auto-save to review manually)\n", name)
		}
	}

	// Notifier events fire even when silent so WebUI / Telegram receive
	// saves regardless of stderr verbosity.
	if sm != nil {
		for _, name := range result.Saved {
			sm.Notifier.Notify(SkillEvent{
				Type:      "saved",
				SkillName: name,
				Timestamp: time.Now().UTC(),
			})
		}
	}

	if len(result.Saved) > 0 && sm != nil {
		sm.MarkDirty()
		sm.Reload()
		runPostSaveCurate(userDir, sm, cfg, llmClient, verbose)
	}
	return true
}

// runPostSaveCurate triggers micro-curation after auto-save. The curator
// only looks at draft-quality skills so it doesn't churn manually-saved
// or already-curated entries.
func runPostSaveCurate(userDir string, sm *SkillManager, cfg SkillsConfig, llmClient LLMClient, verbose io.Writer) {
	allSkills := sm.AllSkills()
	var newSkills []Skill
	for _, s := range allSkills {
		if s.Quality == QualityDraft {
			newSkills = append(newSkills, s)
		}
	}
	msg := RunAutoCurate(userDir, newSkills, allSkills, cfg, llmClient)
	if msg != "" && verbose != nil {
		fmt.Fprint(verbose, msg)
	}
}

// ExtractUserMessages returns the content of every user-role message in
// the conversation. Lives here so the learn-loop and its callers share
// one definition.
func ExtractUserMessages(messages []LlmMessage) []string {
	var out []string
	for _, m := range messages {
		if m.Role == "user" {
			out = append(out, m.Content)
		}
	}
	return out
}
