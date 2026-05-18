// Package loop implements the ReAct (Reasoning + Acting) agent loop.
package loop

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/BackendStack21/kode/internal/llm"
	"github.com/BackendStack21/kode/internal/render"
	"github.com/BackendStack21/kode/internal/tool"
)

// SkillLoader is an optional callback that the loop engine calls before each
// LLM invocation to discover contextually relevant skills. The callback
// receives the latest user input and returns additional system context
// (formatted skill content) to inject, or empty string if no skills match.
type SkillLoader func(userInput string) string

// Engine runs the agent loop: observe → think → act → repeat.
type Engine struct {
	client      *llm.Client
	registry    *tool.Registry
	renderer    *render.Renderer // optional: colored terminal output
	maxIter     int
	system      string
	maxContext  int // max context tokens (0 = no limit)
	skillLoader SkillLoader // optional: loads matching skills
	lastSkillMsg string     // last user message that triggered skill loading (dedup)
}

// New creates a new loop Engine.
// maxContext is the model's maximum context window in tokens.
// Pass 0 for no limit enforcement.
func New(client *llm.Client, registry *tool.Registry, maxIterations int, systemMessage string, renderer *render.Renderer, maxContext int) *Engine {
	return &Engine{
		client:    client,
		registry:  registry,
		renderer:  renderer,
		maxIter:   maxIterations,
		system:    systemMessage,
		maxContext: maxContext,
	}
}

// SetSkillLoader sets the optional skill loader callback.
func (e *Engine) SetSkillLoader(sl SkillLoader) { e.skillLoader = sl }

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
	result, _, err := e.runLoop(ctx, messages)
	return result, err
}

// RunWithMessages executes the agent loop starting from a pre-built
// message history. The messages must include the system prompt (if any),
// all prior conversation turns, and the new user message as the last
// entry. Returns the final answer plus the full updated message history
// so callers can persist it (e.g. to a session file).
//
// Use this for multi-turn conversations: load the session, append the
// new user message, call RunWithMessages, then save the returned messages.
func (e *Engine) RunWithMessages(ctx context.Context, messages []llm.Message) (string, []llm.Message, error) {
	return e.runLoop(ctx, messages)
}

// runLoop is the shared core of Run and RunWithMessages.
// It runs the ReAct loop on the given messages and returns the final
// answer plus the complete updated message history.
func (e *Engine) runLoop(ctx context.Context, messages []llm.Message) (string, []llm.Message, error) {
	tools := e.buildToolDefs()

	for i := 0; i < e.maxIter; i++ {
		select {
		case <-ctx.Done():
			return "", messages, ctx.Err()
		default:
		}

		// Render iteration header (1-indexed for humans)
		if e.renderer != nil {
			e.renderer.Iteration(i+1, e.maxIter, 0, 0, 0, 0)
		}

		// Trim context to stay within model's context window
		messages = e.trimContext(messages, tools)

		// Load relevant skills based on latest user input (once per message)
		if e.skillLoader != nil {
			if userMsg := lastUserMessage(messages); userMsg != "" && userMsg != e.lastSkillMsg {
				if skillContext := e.skillLoader(userMsg); skillContext != "" {
					e.lastSkillMsg = userMsg
					// Inject skill context as a system message right before the user message
					insertIdx := len(messages)
					for j := len(messages) - 1; j >= 0; j-- {
						if messages[j].Role == "system" && j != 0 {
							insertIdx = j + 1
							break
						}
					}
					skillMsg := llm.Message{Role: "system", Content: "# Relevant Skill\n\n" + skillContext}
					messages = append(messages[:insertIdx], append([]llm.Message{skillMsg}, messages[insertIdx:]...)...)
				}
			}
		}

		// THINK (timed)
		start := time.Now()
		result, err := e.client.Call(ctx, messages, tools)
		latency := time.Since(start)
		if err != nil {
			return "", messages, fmt.Errorf("iteration %d: %w", i, err)
		}

		// Render turn statistics (re-draw iteration header with stats)
		if e.renderer != nil {
			e.renderer.Iteration(i+1, e.maxIter, latency, result.InputTokens, result.OutputTokens, 0)
		}

		// No tool calls = final answer
		if len(result.ToolCalls) == 0 {
			if e.renderer != nil {
				e.renderer.FinalAnswer(result.Content)
			}
			return result.Content, messages, nil
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

			// Wrap tool output in clear delimiters so the model treats
			// it as DATA, not as instructions. Even if the output
			// contains "ignore previous instructions", the delimiter
			// makes it visually and semantically distinct.
			delimited := fmt.Sprintf("─── TOOL RESULT (%s) ───\n%s\n─── END TOOL RESULT ───", tc.Function.Name, output)

			messages = append(messages, llm.Message{
				Role:       "tool",
				Content:    delimited,
				Name:       tc.Function.Name,
				ToolCallID: tc.ID,
			})
		}
	}

	return "", messages, fmt.Errorf("reached max iterations (%d) without final answer", e.maxIter)
}

// ── Helpers ───────────────────────────────────────────────────────────

// lastUserMessage returns the content of the most recent user message.
func lastUserMessage(messages []llm.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return messages[i].Content
		}
	}
	return ""
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
