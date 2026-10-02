package main

import (
	"unicode/utf8"

	"github.com/BackendStack21/odek/internal/loop"
	"github.com/BackendStack21/odek/internal/render"
)

// telegramIterationProgress keeps assistant notes separate from the compact
// reasoning preview. Final answers are sent through the ordinary reply path.
func telegramIterationProgress(info loop.IterationInfo) (reasoning, note string) {
	if info.IsPreTool || info.HasFinalAnswer {
		reasoning = render.FirstSentence(info.ReasoningContent)
	}
	if info.IsPreTool {
		note = info.Content
	}
	return
}

// telegramNoteChunks keeps plain-text progress within the same byte limit
// as Telegram response formatting, preserving every byte and rune boundary.
func telegramNoteChunks(note string) []string {
	if note == "" {
		return nil
	}
	text := "💬 " + note
	var chunks []string
	for len(text) > 4096 {
		cut := 4096
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		// Corrupt input must still make progress; valid UTF-8 never takes
		// this fallback, since one rune occupies at most four bytes.
		if cut == 0 {
			cut = 4096
		}
		chunks = append(chunks, text[:cut])
		text = text[cut:]
	}
	return append(chunks, text)
}
