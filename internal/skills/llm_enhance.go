package skills

import (
	"context"
	"fmt"
	"strings"
)

// LLMClient abstracts the LLM calls needed for skill enhancement.
type LLMClient interface {
	SimpleCall(ctx context.Context, system, user string) (string, error)
}

// GenerateSkillWithLLM takes heuristic-detected tool calls and user messages
// and uses the LLM to generate a rich, accurate skill with proper name,
// description, trigger keywords, and structured body.
// Returns nil if the LLM call fails or returns empty output.
func GenerateSkillWithLLM(llm LLMClient, calls []ToolCall, userMessages []string, heuristic string) *SkillSuggestion {
	if llm == nil {
		return nil
	}

	// Build a compact summary of the session for the LLM
	var b strings.Builder
	b.WriteString("Analyze these tool calls from an AI coding agent session:\n\n")

	// Add user messages (last 5)
	msgCount := len(userMessages)
	start := 0
	if msgCount > 5 {
		start = msgCount - 5
	}
	for i := start; i < msgCount; i++ {
		msg := userMessages[i]
		if len(msg) > 200 {
			msg = msg[:197] + "..."
		}
		b.WriteString(fmt.Sprintf("User: %s\n", msg))
	}

	// Add tool calls (capped)
	limit := 10
	if len(calls) < limit {
		limit = len(calls)
	}
	for i := 0; i < limit; i++ {
		c := calls[i]
		input := c.Input
		if len(input) > 120 {
			input = input[:117] + "..."
		}
		status := "✓"
		if c.ExitCode != 0 {
			status = "✗"
		}
		b.WriteString(fmt.Sprintf("[%s] %s: %s\n", status, c.Tool, input))
	}
	if len(calls) > limit {
		b.WriteString(fmt.Sprintf("... and %d more calls\n", len(calls)-limit))
	}

	b.WriteString(fmt.Sprintf("\nHeuristic trigger: %s\n", heuristic))
	b.WriteString("\nGenerate a skill file for this pattern. Output in this exact format:\n")
	b.WriteString("NAME: <short kebab-case name>\n")
	b.WriteString("DESCRIPTION: <one-line description, max 100 chars>\n")
	b.WriteString("TOPICS: <3-5 comma-separated topic keywords>\n")
	b.WriteString("ACTIONS: <2-3 comma-separated action keywords>\n")
	b.WriteString("BODY:\n")
	b.WriteString("<markdown body with ## Overview, ## Step-by-Step, ## Common Pitfalls, ## Verification sections>")

	resp, err := llm.SimpleCall(context.Background(),
		"You are a skill authoring system. Given a session trace, generate a structured skill file. Output the exact format requested. Be concise and specific.",
		b.String(),
	)
	if err != nil || resp == "" {
		return nil
	}

	return parseLLMSuggestion(resp)
}

// parseLLMSuggestion parses the LLM's structured output into a SkillSuggestion.
func parseLLMSuggestion(text string) *SkillSuggestion {
	s := &SkillSuggestion{
		Heuristic: "llm-enhanced",
	}

	lines := strings.Split(text, "\n")
	var inBody bool
	var bodyLines []string

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if strings.HasPrefix(trimmed, "NAME:") {
			s.Name = strings.TrimSpace(strings.TrimPrefix(trimmed, "NAME:"))
		} else if strings.HasPrefix(trimmed, "DESCRIPTION:") {
			s.Description = strings.TrimSpace(strings.TrimPrefix(trimmed, "DESCRIPTION:"))
		} else if strings.HasPrefix(trimmed, "TOPICS:") {
			topics := strings.TrimSpace(strings.TrimPrefix(trimmed, "TOPICS:"))
			if topics != "" {
				for _, t := range strings.Split(topics, ",") {
					t = strings.TrimSpace(t)
					if t != "" {
						s.CommandLog = append(s.CommandLog, "topic:"+t)
					}
				}
			}
		} else if strings.HasPrefix(trimmed, "ACTIONS:") {
			actions := strings.TrimSpace(strings.TrimPrefix(trimmed, "ACTIONS:"))
			if actions != "" {
				for _, a := range strings.Split(actions, ",") {
					a = strings.TrimSpace(a)
					if a != "" {
						s.CommandLog = append(s.CommandLog, "action:"+a)
					}
				}
			}
		} else if strings.HasPrefix(trimmed, "BODY:") {
			inBody = true
		} else if inBody {
			bodyLines = append(bodyLines, line)
		}
	}

	if s.Name == "" || len(bodyLines) == 0 {
		return nil
	}

	s.Body = strings.Join(bodyLines, "\n")
	s.Body = strings.TrimSpace(s.Body)
	return s
}

// EnhanceCurationWithLLM uses the LLM to assess skill quality and suggest
// improvements. Returns a message describing findings, or empty string.
func EnhanceCurationWithLLM(llm LLMClient, report *CurationReport) string {
	if llm == nil || report == nil || report.TotalSkills == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("Review these skills and identify quality issues, overlap, or improvement suggestions:\n\n")
	for _, s := range report.QualityIssues {
		b.WriteString(fmt.Sprintf("- %s: %s\n", s.Name, strings.Join(s.Issues, "; ")))
	}
	for _, g := range report.OverlapGroups {
		b.WriteString(fmt.Sprintf("- Overlap: %s share keywords: %s\n", strings.Join(g.Skills, ", "), strings.Join(g.Shared, ", ")))
	}
	b.WriteString("\nSuggest specific improvements or merge recommendations. Be concise.")

	resp, err := llm.SimpleCall(context.Background(),
		"You are a skill curation assistant. Review the quality report and suggest actionable improvements. Keep it brief.",
		b.String(),
	)
	if err != nil {
		return ""
	}

	resp = strings.TrimSpace(resp)
	if resp == "" {
		return ""
	}
	return resp
}

// ExtractSkillsFromConversation takes the full conversation history (all messages)
// and asks the LLM to identify whether a reusable skill was demonstrated.
// Unlike GenerateSkillWithLLM (which only enhances pattern-detected tool call
// sequences), this analyzes the complete interaction — user intent, agent
// reasoning, tool calls, and final outcome — to discover deeper patterns.
//
// Returns nil if the LLM call fails or no skill is found.
func ExtractSkillsFromConversation(llm LLMClient, messages []LlmMessage, userMessages []string) *SkillSuggestion {
	if llm == nil || len(messages) < 3 {
		return nil
	}

	var b strings.Builder
	b.WriteString("Analyze this AI coding agent conversation for reusable skills:\n\n")

	// Include up to 20 messages (user/assistant/tool) to give full context
	maxMsgs := len(messages)
	if maxMsgs > 20 {
		maxMsgs = 20
	}
	start := len(messages) - maxMsgs
	for i := start; i < len(messages); i++ {
		m := messages[i]
		content := m.Content
		if len(content) > 300 {
			content = content[:297] + "..."
		}
		label := m.Role
		if m.Name != "" {
			label = m.Role + ":" + m.Name
		}
		b.WriteString(fmt.Sprintf("[%s] %s\n", label, content))
	}

	b.WriteString("\nDid the agent demonstrate a reusable skill or procedure? ")
	b.WriteString("If YES, generate a skill file. If NO, respond with just 'none'.\n\n")
	b.WriteString("Output format:\n")
	b.WriteString("NAME: <short kebab-case name>\n")
	b.WriteString("DESCRIPTION: <one-line, max 100 chars>\n")
	b.WriteString("TOPICS: <3-5 comma-separated topic keywords>\n")
	b.WriteString("ACTIONS: <2-3 comma-separated action keywords>\n")
	b.WriteString("BODY:\n")
	b.WriteString("<markdown body with ## Overview, ## Step-by-Step, ## Common Pitfalls, ## Verification>\n")

	resp, err := llm.SimpleCall(context.Background(),
		"You are a skill curator. Given a conversation trace, identify if a reusable skill or procedure was demonstrated. If yes, generate a structured SKILL.md. If no clear reusable pattern exists, respond with just 'none'. Be conservative — only extract genuinely reusable skills.",
		b.String(),
	)
	if err != nil || resp == "" || strings.TrimSpace(resp) == "none" {
		return nil
	}

	s := parseLLMSuggestion(resp)
	if s != nil {
		s.Heuristic = "conversation-extracted"
	}
	return s
}
