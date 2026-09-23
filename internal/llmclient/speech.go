package llmclient

import (
	"context"
	sdk "github.com/BackendStack21/go-llm-sdk"
)

// SpeechRequest/Result mirror the SDK's speech DTOs so callers in cmd/odek
// never import go-llm-sdk directly — same pattern as the chat/vision mapping.

// SpeakRequest describes one text-to-speech conversion.
type SpeakRequest struct {
	Text   string
	Voice  string
	Format string
	Speed  float64 // 0 = omit (provider default)
}

// SpeakResult carries the synthesized audio exactly as the provider
// returned it — no transcoding, no fallback tables.
type SpeakResult struct {
	Audio    []byte
	Model    string
	MIMEType string
}

// TranscribeRequest describes one speech-to-text conversion.
type TranscribeRequest struct {
	Audio    []byte
	Filename string
	MIMEType string
	Language string // optional ISO-639-1 hint
	Prompt   string
	Format   string
}

// TranscribeResult carries the recognized text. Fields the provider omits
// stay zero — unknown data stays unknown.
type TranscribeResult struct {
	Text        string
	Model       string
	Language    string
	DurationSec float64
}

// Speak synthesizes speech with the client's provider and the named model.
// The caller accounts for any returned usage (speech calls report none in v1).
func (c *Client) Speak(ctx context.Context, model string, req SpeakRequest) (*SpeakResult, error) {
	res, err := c.SDK.Speak(ctx, c.ProviderID(), model, sdk.SpeakRequest{
		Text: req.Text, Voice: req.Voice, Format: req.Format, Speed: req.Speed,
	})
	if err != nil {
		return nil, err
	}
	return &SpeakResult{Audio: res.Audio, Model: res.Model, MIMEType: res.MIMEType}, nil
}

// Transcribe converts speech to text with the client's provider and model.
func (c *Client) Transcribe(ctx context.Context, model string, req TranscribeRequest) (*TranscribeResult, error) {
	res, err := c.SDK.Transcribe(ctx, c.ProviderID(), model, sdk.TranscribeRequest{
		Audio: req.Audio, Filename: req.Filename, MIMEType: req.MIMEType,
		Language: req.Language, Prompt: req.Prompt, Format: req.Format,
	})
	if err != nil {
		return nil, err
	}
	return &TranscribeResult{Text: res.Text, Model: res.Model, Language: res.Language, DurationSec: res.DurationSec}, nil
}
