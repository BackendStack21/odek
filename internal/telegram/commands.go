package telegram

import (
	"fmt"
	"strings"
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
	}
}

func startHandler(args string) (string, error) {
	return "🤖 *odek Telegram Bot*\n\n" +
		"I am odek — an expert software engineer who ships.\n\n" +
		"Available commands:\n" +
		"/help — Show available commands\n" +
		"/new — Reset conversation\n" +
		"/stats — Show session statistics\n" +
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
	return "📊 *Session Stats*\n\n" +
		"Messages: {count}\n" +
		"Session started: {time}\n\n" +
		"(Dynamic stats not available yet — connect to session store)", nil
}

func stopHandler(args string) (string, error) {
	return "⏹️ Stop requested. Current task has been cancelled.", nil
}

func modeHandler(args string) (string, error) {
	return "⚙️ *Agent Modes*\n\nSelect a mode to toggle:", nil
}

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
