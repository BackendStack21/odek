package odek

import (
	"fmt"
	"os"
	"runtime"
	"time"
)

// BuildRuntimeContext returns a system prompt header with OS, hostname,
// working directory, current date/time, and platform-specific formatting
// rules for the given transport (platform). platform can be "telegram",
// "terminal", "web", or empty for generic.
//
// This context eliminates the need for the agent to run shell commands
// to discover its own environment — the most common waste of tokens in
// CLI agent usage.
func BuildRuntimeContext(platform string) string {
	hostname, _ := os.Hostname()
	cwd, _ := os.Getwd()
	now := time.Now()

	ctx := fmt.Sprintf(
		"Host: %s (%s)\nUser home directory: %s\nCurrent working directory: %s\nDate: %s",
		runtime.GOOS, hostname, os.Getenv("HOME"), cwd,
		now.Format("Monday, January 2, 2006 15:04 MST"),
	)

	switch platform {
	case "telegram":
		ctx += "\n\nYou are on a text messaging communication platform, Telegram. " +
			"Standard markdown is supported: **bold**, *italic*, ~~strikethrough~~, " +
			"||spoiler||, `inline code`, ```code blocks```, [links](url), and ## headers. " +
			"Use send_message for Telegram-specific features like inline keyboard buttons " +
			"(via buttons parameter with cb: prefix)."

	case "web":
		ctx += "\n\nYou are running in a web UI. " +
			"Responses are streamed to the browser via WebSocket. " +
			"Markdown formatting is supported. Keep responses concise and visual."

	case "terminal", "":
		ctx += "\n\nYou are running in a terminal/CLI environment. " +
			"Output is plain text with ANSI color codes. " +
			"Keep responses concise — the user is at a shell prompt."
	}

	return ctx
}
