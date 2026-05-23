package telegram

import (
	"fmt"
	"strings"
	"time"
)

// CommandDescriptor describes a slash command and its handler.
type CommandDescriptor struct {
	Command     string
	Description string
	Handler     func(args string) (string, error)
}

// DefaultCommands is the built-in list of slash commands.
// Populated via init() to avoid initialization cycle with handler functions
// that reference the variable.
var DefaultCommands []CommandDescriptor

func init() {
	DefaultCommands = []CommandDescriptor{
		{
			Command:     "start",
			Description: "Start the bot and see welcome message",
			Handler:     startHandler,
		},
		{
			Command:     "help",
			Description: "Show available commands and usage",
			Handler:     helpHandler,
		},
		{
			Command:     "new",
			Description: "Reset conversation (clear context)",
			Handler:     newHandler,
		},
		{
			Command:     "stats",
			Description: "Show session statistics",
			Handler:     statsHandler,
		},
		{
			Command:     "stop",
			Description: "Cancel running agent task",
			Handler:     stopHandler,
		},
		{
			Command:     "mode",
			Description: "Toggle agent modes (sandbox, verbose)",
			Handler:     modeHandler,
		},
		{
			Command:     "restart",
			Description: "Restart the bot process gracefully",
			Handler:     restartHandler,
		},
		{
			Command:     "sessions",
			Description: "List recent conversation sessions",
			Handler:     sessionsHandler,
		},
		{
			Command:     "resume",
			Description: "Resume a previous session by ID",
			Handler:     resumeHandler,
		},
		{
			Command:     "prune",
			Description: "Clean up old sessions (default: 30 days)",
			Handler:     pruneHandler,
		},
		{
			Command:     "plan",
			Description: "Create a new plan from a description",
			Handler:     planHandler,
		},
		{
			Command:     "plans",
			Description: "List all saved plans",
			Handler:     plansHandler,
		},
		{
			Command:     "plan_view",
			Description: "View a plan's full content by slug",
			Handler:     planViewHandler,
		},
		{
			Command:     "plan_delete",
			Description: "Delete a plan by slug",
			Handler:     planDeleteHandler,
		},
		{
			Command:     "plan_resume",
			Description: "Resume the most recent plan",
			Handler:     planResumeHandler,
		},
	}
}

func startHandler(args string) (string, error) {
	return "🤖 *odek Telegram Bot*\n\n" +
		"I am odek — an expert software engineer who ships.\n\n" +
		"Available commands:\n" +
		"/help — Show available commands\n" +
		"/new — Reset conversation\n" +
		"/stats — Show session statistics\n" +
		"/sessions — List recent sessions\n" +
		"/resume <id> — Resume a previous session\n" +
		"/prune [days] — Clean up old sessions\n" +
		"/stop — Cancel running task\n\n" +
		"Send me a message and I will help!", nil
}

func helpHandler(args string) (string, error) {
	var b strings.Builder
	b.WriteString("📋 *Available Commands*\n\n")
	for _, cmd := range DefaultCommands {
		fmt.Fprintf(&b, "/%s — %s\n", cmd.Command, cmd.Description)
	}
	return b.String(), nil
}

func newHandler(args string) (string, error) {
	return "🔄 Conversation reset. Starting fresh.", nil
}

func statsHandler(args string) (string, error) {
	return "📊 *Stats* — Session statistics are displayed inline by the bot.", nil
}

func stopHandler(args string) (string, error) {
	return "⏹️ Stop requested. Current task has been cancelled.", nil
}

func modeHandler(args string) (string, error) {
	return "⚙️ *Agent Modes*\n\n" +
		"Modes are set at startup via `odek.json` or CLI flags:\n" +
		"• `interaction_mode: engaging` — emoji-rich narration (default)\n" +
		"• `interaction_mode: enhance` — narrated tool summaries (persist)\n" +
		"• `interaction_mode: verbose` — raw tool call output\n" +
		"• `sandbox: true` — run in Docker isolation\n" +
		"• `skills.verbose: true` — show skill learning details\n\n" +
		"Restart the bot after changing config.", nil
}

// restartHandler handles the /restart command.
// The actual restart signal is sent by the caller (telegramCmd) after
// this response is delivered to the chat. This handler just returns
// a confirmation message — the caller sends SIGHUP to trigger restart.
func restartHandler(args string) (string, error) {
	return "🔄 *Restarting...*\n\nThe bot will restart momentarily. This may take a few seconds.", nil
}

func sessionsHandler(args string) (string, error) { return "", nil }

func resumeHandler(args string) (string, error) { return "", nil }

func pruneHandler(args string) (string, error) { return "", nil }

// ── Plan Command Handlers ──────────────────────────────────────────────

func planHandler(args string) (string, error) { return "", nil }

func plansHandler(args string) (string, error) {
	infos, err := ListPlans(20)
	if err != nil {
		return fmt.Sprintf("❌ Failed to list plans: %v", err), nil
	}
	if len(infos) == 0 {
		return "📋 *Plans* — No plans found.\n\nCreate one with `/plan <description>`", nil
	}
	var b strings.Builder
	b.WriteString("📋 *Plans*\n\n")
	for _, p := range infos {
		ago := time.Since(p.ModTime).Round(time.Minute)
		fmt.Fprintf(&b, "`%s` — %s ago\n", p.Slug, ago)
		if p.Preview != "" {
			fmt.Fprintf(&b, "  _%s_\n", truncateStr(p.Preview, 60))
		}
	}
	b.WriteString("\nUse `/plan_view <slug>` to read a plan.")
	return b.String(), nil
}

func planViewHandler(args string) (string, error) {
	slug := strings.TrimSpace(args)
	if slug == "" {
		return "❗ Usage: `/plan_view <slug>`\n\nUse `/plans` to see available plans.", nil
	}
	matched, content, err := ReadPlan(slug)
	if err != nil {
		return fmt.Sprintf("❌ %v", err), nil
	}
	// Telegram messages have a 4096 char limit. Truncate if needed.
	if len(content) > 3900 {
		content = content[:3900] + "\n\n… _(truncated — plan too long for Telegram)_"
	}
	return fmt.Sprintf("📄 *Plan: `%s`*\n\n%s", matched, content), nil
}

func planDeleteHandler(args string) (string, error) {
	slug := strings.TrimSpace(args)
	if slug == "" {
		return "❗ Usage: `/plan_delete <slug>`\n\nUse `/plans` to see available plans.", nil
	}
	matched, err := DeletePlan(slug)
	if err != nil {
		return fmt.Sprintf("❌ %v", err), nil
	}
	return fmt.Sprintf("🗑️ *Plan deleted*: `%s`", matched), nil
}

func planResumeHandler(args string) (string, error) { return "", nil }

// FindCommand returns the command descriptor with the matching name, or nil.
func FindCommand(name string) *CommandDescriptor {
	for i := range DefaultCommands {
		if DefaultCommands[i].Command == name {
			return &DefaultCommands[i]
		}
	}
	return nil
}

// CommandDescriptors returns a slice of BotCommand suitable for the
// Telegram SetMyCommands API.
func CommandDescriptors() []BotCommand {
	descs := make([]BotCommand, len(DefaultCommands))
	for i, cmd := range DefaultCommands {
		descs[i] = BotCommand{
			Command:     cmd.Command,
			Description: cmd.Description,
		}
	}
	return descs
}

// truncateStr shortens s to maxLen, appending "…" if trimmed.
func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "…"
}
