package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/BackendStack21/odek/internal/budget"
	"github.com/BackendStack21/odek/internal/config"
	"github.com/BackendStack21/odek/internal/danger"
	"github.com/BackendStack21/odek/internal/events"
	"github.com/BackendStack21/odek/internal/llmclient"
)

type visionBudgetOwner interface {
	budget.View
	ReserveInferenceBudget() (budget.Grant, error)
	SettleExternalBudget(budget.Grant, *budget.Usage)
}

// providerVisionAnalyzer retains resolved operator settings; it never reads
// credentials from the environment after startup scrubbing.
type providerVisionAnalyzer struct {
	options   llmclient.Options
	provider  string
	maxTokens int
	limits    budget.Limits
	view      budget.View
	emit      func(events.Event)
}

func (a *providerVisionAnalyzer) SetBudgetView(v budget.View)           { a.view = v }
func (a *providerVisionAnalyzer) SetEventEmitter(fn func(events.Event)) { a.emit = fn }

func (a *providerVisionAnalyzer) AnalyzeVision(ctx context.Context, model, prompt string, media []visionMedia) (visionAnalysis, error) {
	if err := ctx.Err(); err != nil {
		return visionAnalysis{}, err
	}
	s, err := llmclient.NewSDK(a.options)
	if err != nil {
		return visionAnalysis{}, fmt.Errorf("vision: invalid provider configuration")
	}
	c, err := llmclient.New(s, a.provider, model)
	if err != nil {
		return visionAnalysis{}, fmt.Errorf("vision: configured provider is unavailable")
	}
	timeout := a.options.Timeout
	if timeout <= 0 {
		timeout = llmclient.DefaultTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	maxTokens := a.maxTokens
	if maxTokens <= 0 {
		maxTokens = config.DefaultVisionMaxTokens
	}
	inputPrice, outputPrice := a.limits.ResolvePrices(model)
	if a.limits.MaxCostUSD > 0 && (inputPrice <= 0 || outputPrice <= 0) {
		return visionAnalysis{}, fmt.Errorf("vision: configure input and output prices for the vision model before using a cost budget")
	}
	var usage *budget.Usage
	if owner, ok := a.view.(visionBudgetOwner); ok {
		grant, e := owner.ReserveInferenceBudget()
		if e != nil {
			return visionAnalysis{}, e
		}
		// Known zero until the request is submitted. Failed preflight never
		// consumes a grant; unknown usage after submission is charged conservatively.
		usage = &budget.Usage{CostKnown: true}
		defer func() { owner.SettleExternalBudget(grant, usage) }()
		if grant.Limits.MaxOutputTokens > 0 {
			maxTokens = min(maxTokens, int(grant.Limits.MaxOutputTokens))
		}
		if grant.Limits.MaxRuntimeSeconds > 0 {
			var stop context.CancelFunc
			ctx, stop = context.WithTimeout(ctx, time.Duration(grant.Limits.MaxRuntimeSeconds)*time.Second)
			defer stop()
		}
		if grant.Limits.MaxCostUSD > 0 {
			// A grant-level cap without configured prices is unenforceable
			// (affordable would divide by zero → +Inf); reject instead of
			// silently bypassing the cap.
			if inputPrice <= 0 || outputPrice <= 0 {
				return visionAnalysis{}, fmt.Errorf("vision: configure input and output prices for the vision model before using a cost budget")
			}
			affordable := math.Floor(grant.Limits.MaxCostUSD * 1e6 / outputPrice)
			if affordable < 1 {
				return visionAnalysis{}, &budget.Error{Limit: budget.LimitCostUSD}
			}
			if affordable < float64(maxTokens) {
				maxTokens = int(affordable)
			}
		}
	}
	images := make([]llmclient.ImageInput, 0, len(media))
	for _, im := range media {
		images = append(images, llmclient.ImageInput{MIME: im.MIME, Data: im.Data})
	}
	if len(images) == 0 {
		return visionAnalysis{}, fmt.Errorf("vision: no images supplied")
	}
	usage = nil
	result, callErr := c.AnalyzeImages(ctx, prompt, images, maxTokens)
	if result != nil {
		u := budget.Usage{InputTokens: int64(result.InputTokens), OutputTokens: int64(result.OutputTokens), CacheReadTokens: int64(result.CacheReadTokens), CacheCreationTokens: int64(result.CacheCreationTokens)}
		u.CostKnown = inputPrice > 0 && outputPrice > 0
		u.CostUSD = (float64(u.TotalInput())*inputPrice + float64(u.OutputTokens)*outputPrice) / 1e6
		if u.Valid() && (u.TotalInput() > 0 || u.OutputTokens > 0) {
			usage = &u
		}
	}
	if usage == nil && callErr != nil {
		// The request was submitted; the provider may have processed tokens
		// even though we never saw usage. Charge the output-token budget
		// conservatively so an error path cannot settle zero.
		usage = &budget.Usage{OutputTokens: int64(maxTokens), CostKnown: inputPrice > 0 && outputPrice > 0}
		if usage.CostKnown {
			usage.CostUSD = float64(usage.OutputTokens) * outputPrice / 1e6
		}
	}
	if a.emit != nil {
		data := map[string]any{"kind": "vision", "model": model, "provider": a.provider, "usage_known": usage != nil}
		if usage != nil {
			data["input_tokens"] = usage.TotalInput()
			data["output_tokens"] = usage.OutputTokens
			data["cost_usd"] = usage.CostUSD
			data["cost_known"] = usage.CostKnown
		}
		a.emit(events.Event{Type: events.TypeSideCallUsage, Data: data})
	}
	if callErr != nil {
		if errors.Is(callErr, context.Canceled) || errors.Is(callErr, context.DeadlineExceeded) {
			return visionAnalysis{}, callErr
		}
		// Provider errors can echo base64 request bodies; never forward them into
		// tool results, sessions, or logs.
		return visionAnalysis{}, fmt.Errorf("vision: provider request failed; check model image support and provider configuration")
	}
	if result == nil || result.Content == "" {
		return visionAnalysis{}, fmt.Errorf("vision: provider returned no description")
	}
	return visionAnalysis{Text: result.Content, Model: c.Model()}, nil
}

func newConfiguredVisionTool(dc danger.DangerousConfig, tc toolConfig, approver danger.Approver) *visionTool {
	t := newVisionTool(dc, tc.Vision)
	t.SetApprover(approver)
	if tc.Vision.Backend == config.VisionBackendProvider {
		provider := tc.Vision.Provider
		if provider == "" {
			provider = tc.Provider
		}
		t.SetAnalyzer(&providerVisionAnalyzer{options: tc.VisionOptions, provider: provider, maxTokens: tc.Vision.MaxTokens, limits: tc.VisionLimits})
	}
	return t
}
