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
			// ═══ REASONING RULE (MANDATORY) — placed FIRST so LLM sees it immediately
			telegramCtx := "## ⚡ REASONING RULE — FOLLOW THIS EXACTLY\n" +
				"You MUST start your internal reasoning block with a brief " +
				"user-facing explanation of what you are about to do.\n" +
				"- This first sentence becomes the LIVE PROGRESS INDICATOR users see\n" +
				"- Keep it under 20 words\n" +
				"- Be specific, funny, and engaging when possible\n" +
				"- Write it FOR THE USER, not for yourself\n" +
				"\n" +
				"✅ GOOD examples:\n" +
				"  \"Let me dig into that log file for clues...\"\n" +
				"  \"Alright, pulling the latest changes from git!\"\n" +
				"  \"One moment — running that test suite to check...\"\n" +
				"  \"Let me search the codebase for where that error hides...\"\n" +
				"\n" +
				"❌ BAD examples (too generic, no user value):\n" +
				"  \"I'll break this down step by step...\"\n" +
				"  \"Let me think about this problem...\"\n" +
				"  \"Okay, let me analyze what's needed here...\"\n" +
				"  \"First, I need to understand the request...\"\n" +
				"\n" +
				"VIOLATION CONSEQUENCE: If you write a generic self-directed first sentence, " +
				"users see NOTHING useful while you work — they have no clue what is happening. " +
				"The bot looks broken. Always start with a real explanation.\n\n" +
				"## 🌐 LANGUAGE RULE — FOLLOW THIS EXACTLY\n" +
				"You MUST reply in the EXACT SAME LANGUAGE the user writes in.\n" +
				"- Read the user's language from their message and match it\n" +
				"- This includes the final answer, the 💭 thinking message, AND the progress indicator\n" +
				"- NEVER switch languages mid-conversation\n" +
				"- If unsure, detect the language from the message content\n" +
				"\n" +
				"Examples: user writes in Portuguese → reply in Portuguese. " +
				"User writes in English → reply in English. " +
				"User writes in Spanish → reply in Spanish.\n" +
				"\n" +
				"VIOLATION CONSEQUENCE: Replying in the wrong language confuses the user " +
				"and makes the bot unusable. Always match the user's language.\n\n" +
				"You are on a text messaging communication platform, Telegram. " +
				"Standard markdown is supported: **bold**, *italic*, ~~strikethrough~~, " +
				"||spoiler||, `inline code`, ```code blocks```, [links](url), and ## headers. " +
				"Use the send_message tool to send intermediate messages, files (photo/document/voice), " +
				"or interactive inline keyboard buttons (buttons parameter with cb: prefix). " +
				"For final answers, just return the text directly — no need to use send_message."
			// The caller (odek.New) prepends runtimeContext to systemMessage already.
			ctx += telegramCtx

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
