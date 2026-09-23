package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/llmclient"
	"github.com/BackendStack21/odek/internal/telegram"
)

// sendTelegramVoiceReply synthesizes the final answer text as a voice note
// and delivers it through the existing chat-scoped media path. The voice
// note is strictly additive — the caller has always already delivered the
// text answer — so any synthesis or send failure is returned only for
// logging and must never fail the turn.
func sendTelegramVoiceReply(bot *telegram.Bot, chatID int64, text string, tts config.TTSConfig, speech speechBackend) error {
	if speech == nil || text == "" || tts.Backend != config.TTSBackendProvider {
		return nil
	}

	maxChars := tts.MaxChars
	if maxChars <= 0 {
		maxChars = config.DefaultTTSMaxChars
	}
	// Bound the synthesized payload at the operator's max_chars: a long
	// answer must not turn into an unbounded (or rate-limit-tripping) TTS
	// request. Hard byte truncation; an invalid rune at the cut is dropped.
	payload := text
	if len(payload) > maxChars {
		payload = strings.ToValidUTF8(payload[:maxChars], "")
	}

	res, err := speech.SpeakAudio(context.Background(), payload)
	if err != nil {
		return fmt.Errorf("tts synthesize: %w", err)
	}
	if len(res.Audio) == 0 {
		return fmt.Errorf("tts synthesize: provider returned empty audio")
	}

	// Write the audio into the chat media dir using the same chat-scoped
	// naming convention as downloaded media, so the outbound allowlist
	// (ResolveMediaPathForChat) accepts the file for this chat only.
	dir, err := telegram.MediaDir()
	if err != nil {
		return err
	}
	ext := speakExt(res.MIMEType, tts.Format)
	if ext == "" {
		ext = "mp3"
	}
	outPath := filepath.Join(dir, fmt.Sprintf("voice_chat%d_reply.%s", chatID, ext))
	if err := os.WriteFile(outPath, res.Audio, 0o600); err != nil {
		return fmt.Errorf("tts write audio: %w", err)
	}

	// Reuse the existing media delivery path (SendVoice + allowlist).
	err = sendTelegramMedia(bot, chatID, "voice", outPath, "", nil)

	// The synthesized file is consumed immediately; remove it so the media
	// directory does not accumulate one audio copy per turn. A removal
	// failure must not mask the delivery result.
	if rmErr := os.Remove(outPath); rmErr != nil && err == nil {
		err = fmt.Errorf("tts cleanup audio: %w", rmErr)
	}
	return err
}

// telegramSpeechOptions builds the LLM client options for the voice-reply
// speech client from the resolved Telegram config, mirroring
// toolConfigFromResolved's SpeechOptions so both surfaces share one
// provider/credential resolution.
func telegramSpeechOptions(resolved config.ResolvedConfig, requestTimeoutSeconds int) llmclient.Options {
	var timeout time.Duration
	if requestTimeoutSeconds > 0 {
		timeout = time.Duration(requestTimeoutSeconds) * time.Second
	}
	return llmclient.Options{
		Provider:  resolved.Provider,
		APIKey:    resolved.APIKey,
		BaseURL:   resolved.BaseURL,
		Providers: resolved.ProviderOverrides(),
		Timeout:   timeout,
	}
}
