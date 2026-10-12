package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	sdk "github.com/BackendStack21/go-llm-sdk"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/llmclient"
)

// speechBackend is the provider-backed speech surface shared by the speak
// tool and the provider STT dispatch. Voice/format/speed and the STT model
// come from resolved operator config — callers never pick them per call.
type speechBackend interface {
	SpeakAudio(ctx context.Context, text string) (*llmclient.SpeakResult, error)
	TranscribeAudio(ctx context.Context, filename string, audio []byte, language string) (*llmclient.TranscribeResult, error)
}

// providerSpeechClient retains resolved operator settings; it never reads
// credentials from the environment after startup scrubbing. Mirrors the
// provider vision analyzer: provider registry for credentials, timeout
// inheritance bounded by the remaining run budget, and errors that never
// leak API keys.
type providerSpeechClient struct {
	options llmclient.Options
	view    budget.View

	// TTS settings (from the resolved tts section).
	ttsProvider string
	ttsModel    string
	voice       string
	format      string
	speed       float64

	// STT settings (from the resolved stt section; provider mode only).
	sttProvider string
	sttModel    string
}

func (p *providerSpeechClient) SetBudgetView(v budget.View) { p.view = v }

// speakTimeout resolves the request timeout: the operator's llm
// request_timeout_seconds (already stamped into options.Timeout), bounded by
// the remaining run-budget runtime when a budget view is available.
func (p *providerSpeechClient) speakTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel, _ := p.speakBudgeted(ctx)
	return ctx, cancel
}

// speechTokenEstimate approximates the token count of speech text at four
// characters per token. The provider SDK reports no usage for speech calls, so
// this is the charge recorded against the run budget in its place.
func speechTokenEstimate(chars int) int64 { return int64((chars + 3) / 4) }

// speakBudgeted is speakTimeout plus a charge function: the usage passed to it
// is what the settled grant records (zero cost, known), so a speech call is
// always accounted for.
func (p *providerSpeechClient) speakBudgeted(ctx context.Context) (context.Context, context.CancelFunc, func(budget.Usage)) {
	usage := budget.Usage{CostKnown: true}
	charge := func(u budget.Usage) { usage = u }
	timeout := p.options.Timeout
	if timeout <= 0 {
		timeout = llmclient.DefaultTimeout
	}
	var settle func()
	if owner, ok := p.view.(visionBudgetOwner); ok {
		if grant, err := owner.ReserveInferenceBudget(); err == nil {
			// The reservation only supplies the remaining runtime; release it
			// when the call finishes so it never keeps withholding budget.
			settle = func() { u := usage; owner.SettleExternalBudget(grant, &u) }
			if grant.Limits.MaxRuntimeSeconds > 0 {
				if bound := time.Duration(grant.Limits.MaxRuntimeSeconds) * time.Second; bound < timeout {
					timeout = bound
				}
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	if settle == nil {
		return ctx, cancel, charge
	}
	var once sync.Once
	return ctx, func() {
		cancel()
		once.Do(settle)
	}, charge
}

// speechError maps SDK failures to actionable tool errors. ConfigError
// messages are operator-actionable and key-free by SDK invariant; everything
// else is collapsed to a generic provider failure so keys and transport
// details can never leak into the transcript.
func speechError(prefix string, err error) error {
	if err == nil {
		return nil
	}
	var ce *sdk.ConfigError
	if errors.As(err, &ce) {
		return fmt.Errorf("%s: %s", prefix, ce.Msg)
	}
	return fmt.Errorf("%s: provider request failed; check provider configuration and model support", prefix)
}

func (p *providerSpeechClient) SpeakAudio(ctx context.Context, text string) (*llmclient.SpeakResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := llmclient.NewSDK(p.options)
	if err != nil {
		return nil, fmt.Errorf("tts: invalid provider configuration")
	}
	c, err := llmclient.New(s, p.ttsProvider, p.ttsModel)
	if err != nil {
		return nil, fmt.Errorf("tts: configured provider is unavailable")
	}
	ctx, cancel, charge := p.speakBudgeted(ctx)
	defer cancel()
	charge(budget.Usage{InputTokens: speechTokenEstimate(len(text)), CostKnown: true})
	res, err := c.Speak(ctx, p.ttsModel, llmclient.SpeakRequest{
		Text: text, Voice: p.voice, Format: p.format, Speed: p.speed,
	})
	if err != nil {
		return nil, speechError("tts", err)
	}
	return res, nil
}

func (p *providerSpeechClient) TranscribeAudio(ctx context.Context, filename string, audio []byte, language string) (*llmclient.TranscribeResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s, err := llmclient.NewSDK(p.options)
	if err != nil {
		return nil, fmt.Errorf("stt: invalid provider configuration")
	}
	c, err := llmclient.New(s, p.sttProvider, p.sttModel)
	if err != nil {
		return nil, fmt.Errorf("stt: configured provider is unavailable")
	}
	ctx, cancel, charge := p.speakBudgeted(ctx)
	defer cancel()
	// Audio has no character count; the transcript length is the estimate.
	charge(budget.Usage{CostKnown: true})
	res, err := c.Transcribe(ctx, p.sttModel, llmclient.TranscribeRequest{
		Audio:    audio,
		Filename: filename,
		Language: language,
	})
	if err != nil {
		return nil, speechError("stt", err)
	}
	charge(budget.Usage{OutputTokens: speechTokenEstimate(len(res.Text)), CostKnown: true})
	return res, nil
}

// newSpeechClient builds the shared speech client from a resolved config and
// tool options. Returns nil unless a speech backend is configured (tts
// provider mode enables SpeakAudio; stt provider mode enables
// TranscribeAudio) — callers gate tool registration on the returned flags.
func newSpeechClient(tts config.TTSConfig, stt config.STTConfig, options llmclient.Options) *providerSpeechClient {
	c := &providerSpeechClient{options: options}
	if tts.Backend == config.TTSBackendProvider {
		c.ttsProvider = tts.Provider
		c.ttsModel = tts.Model
		c.voice = tts.Voice
		c.format = tts.Format
		c.speed = tts.Speed
	}
	if stt.Backend == config.STTBackendProvider {
		c.sttProvider = stt.Provider
		c.sttModel = stt.Model
	}
	if c.ttsProvider == "" && c.sttProvider == "" {
		return nil
	}
	return c
}

var _ speechBackend = (*providerSpeechClient)(nil)
