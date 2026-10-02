package main

import (
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
