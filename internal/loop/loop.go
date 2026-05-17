// Package loop implements the ReAct (Reasoning + Acting) agent loop.
package loop

import (
	"context"
	"fmt"
	"strings"

	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/tool"
)

// Engine runs the agent loop: observe → think → act → repeat.
type Engine struct {
	client   *llm.Client
	registry *tool.Registry
	maxIter  int
	system   string
}

// New creates a new loop Engine.
func New(client *llm.Client, registry *tool.Registry, maxIterations int, systemMessage string) *Engine {
	return &Engine{
		client:   client,
		registry: registry,
		maxIter:  maxIterations,
		system:   systemMessage,
	}
}

// Run executes the loop for a given task and returns the final response.
func (e *Engine) Run(ctx context.Context, task string) (string, error) {
	messages := []llm.Message{
		{Role: "user", Content: task},
	}
	if e.system != "" {
		messages = append([]llm.Message{{Role: "system", Content: e.system}}, messages...)
	}

	tools := e.buildToolDefs()

	for i := 0; i < e.maxIter; i++ {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		// THINK
		result, err := e.client.Call(ctx, messages, tools)
		if err != nil {
			return "", fmt.Errorf("iteration %d: %w", i, err)
		}

		// No tool calls = final answer
		if len(result.ToolCalls) == 0 {
			return result.Content, nil
		}

		// Build assistant message with tool calls
		assistantMsg := llm.Message{
			Role:      "assistant",
			Content:   result.Content,
			ToolCalls: result.ToolCalls,
		}
		messages = append(messages, assistantMsg)

		// ACT: execute each tool call
		for _, tc := range result.ToolCalls {
			t := e.registry.Get(tc.Function.Name)
			output := fmt.Sprintf("error: tool %q not found", tc.Function.Name)
			if t != nil {
				res, err := t.Call(tc.Function.Arguments)
				if err != nil {
					output = fmt.Sprintf("error: %s", err.Error())
				} else {
					output = res
				}
			}

			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    output,
				Name:       tc.Function.Name,
				ToolCallID: tc.ID,
			})
		}
	}

	return "", fmt.Errorf("reached max iterations (%d) without final answer", e.maxIter)
}

// buildToolDefs converts the registry's tools to LLM-compatible definitions.
func (e *Engine) buildToolDefs() []llm.ToolDef {
	all := e.registry.Tools()
	defs := make([]llm.ToolDef, 0, len(all))
	for _, t := range all {
		schema := t.Schema()
		var params any
		if s, ok := schema.(string); ok {
			if strings.TrimSpace(s) != "" {
				params = map[string]any{"type": "object", "raw_schema": s}
			} else {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
		} else {
			params = schema
		}

		defs = append(defs, llm.ToolDef{
			Type: "function",
			Function: llm.FunctionDef{
				Name:        t.Name(),
				Description: t.Description(),
				Parameters:  params,
			},
		})
	}
	return defs
}
