// Package loop implements the ReAct (Reasoning + Acting) agent loop.
package loop

import (
	"context"
	"fmt"
	"strings"

	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/render"
	"github.com/BackendStack21/kode/internal/tool"
)

// Engine runs the agent loop: observe → think → act → repeat.
type Engine struct {
	client     *llm.Client
	registry   *tool.Registry
	renderer   *render.Renderer // optional: colored terminal output
	maxIter    int
	system     string
	maxContext int // max context tokens (0 = no limit)
}

// New creates a new loop Engine.
// maxContext is the model's maximum context window in tokens.
// Pass 0 for no limit enforcement.
func New(client *llm.Client, registry *tool.Registry, maxIterations int, systemMessage string, renderer *render.Renderer, maxContext int) *Engine {
	return &Engine{
		client:     client,
		registry:   registry,
		renderer:   renderer,
		maxIter:    maxIterations,
		system:     systemMessage,
		maxContext: maxContext,
	}
}

// ── Token Estimation ─────────────────────────────────────────────────
//
// Zero-dependency heuristic: 1 token ≈ 4 chars for English text.
// JSON structure overhead is estimated per message and per tool call.
// These are conservative overestimates to prevent context limit errors.

// messageOverhead is the estimated tokens for JSON framing around a message.
const messageOverhead = 50

// toolCallOverhead is the estimated tokens for JSON framing around a tool call.
const toolCallOverhead = 30

// contextSafetyMargin is the fraction of MaxContext reserved for output.
// Input (messages + tools) should not exceed this fraction.
const contextSafetyMargin = 0.75

// estimateTokens returns a rough upper-bound token count for a string.
// Conservative: ~4 chars per token. Dense content (code, JSON) is
// closer to 2-3 chars/token; this is safe for both.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// estimateMessages returns the estimated total tokens for a slice of messages.
func estimateMessages(messages []llm.Message) int {
	total := 0
	for _, m := range messages {
		total += messageOverhead
		total += estimateTokens(m.Content)
		total += estimateTokens(m.Name)
		total += estimateTokens(m.ToolCallID)
		for _, tc := range m.ToolCalls {
			total += toolCallOverhead
			total += estimateTokens(tc.ID)
			total += estimateTokens(tc.Function.Name)
			total += estimateTokens(tc.Function.Arguments)
		}
	}
	return total
}

// estimateToolDefs returns the estimated tokens for tool definitions.
// These are sent with every request and count toward the context budget.
func estimateToolDefs(defs []llm.ToolDef) int {
	total := 0
	for _, d := range defs {
		total += 30 // tool definition overhead
		total += estimateTokens(d.Type)
		total += estimateTokens(d.Function.Name)
		total += estimateTokens(d.Function.Description)
	}
	return total
}

// contextBudget returns the input token budget (fraction of MaxContext).
func contextBudget(maxContext int) int {
	if maxContext <= 0 {
		return 0 // no limit
	}
	return int(float64(maxContext) * contextSafetyMargin)
}

// ── Context Trimming ─────────────────────────────────────────────────

// trimContext trims the message history to stay within the context budget.
// It preserves:
//   - System message (always first, if present)
//   - The first user message (the original task)
//
// It drops the oldest non-essential messages (tool call / tool result
// pairs) until estimated tokens fit within the budget.
func (e *Engine) trimContext(messages []llm.Message, toolDefs []llm.ToolDef) []llm.Message {
	budget := contextBudget(e.maxContext)
	if budget <= 0 {
		return messages
	}

	// Estimate tool definitions once (they don't change between iterations)
	defTokens := estimateToolDefs(toolDefs)

	for {
		msgTokens := estimateMessages(messages)
		if msgTokens+defTokens <= budget {
			break
		}
		if len(messages) <= 2 {
			break // can't trim further (need system + task at minimum)
		}

		// Find the first droppable message index.
		// Keep messages[0] if it's the system message.
		// Keep the next message too (first user message = the task).
		dropIdx := 0
		if messages[0].Role == "system" {
			dropIdx = 1 // keep system
		}
		// Keep the message after system too (it's the task)
		dropIdx++ // keep system + task
		if dropIdx >= len(messages) {
			break
		}

		// Drop this message (oldest non-essential)
		messages = append(messages[:dropIdx], messages[dropIdx+1:]...)
	}
	return messages
}

// ── Loop ──────────────────────────────────────────────────────────────

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

		// Render iteration header (1-indexed for humans)
		if e.renderer != nil {
			e.renderer.Iteration(i+1, e.maxIter)
		}

		// Trim context to stay within model's context window
		messages = e.trimContext(messages, tools)

		// THINK
		result, err := e.client.Call(ctx, messages, tools)
		if err != nil {
			return "", fmt.Errorf("iteration %d: %w", i, err)
		}

		// No tool calls = final answer
		if len(result.ToolCalls) == 0 {
			if e.renderer != nil {
				e.renderer.FinalAnswer(result.Content)
			}
			return result.Content, nil
		}

		// Render the model's thinking (reasoning before tool calls)
		if e.renderer != nil && result.Content != "" {
			e.renderer.Thinking(result.Content)
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
			if e.renderer != nil {
				e.renderer.ToolCall(tc.Function.Name, tc.Function.Arguments)
			}

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

			if e.renderer != nil {
				e.renderer.ToolResult(output)
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
