package danger

import "strings"

// envAssignmentCodeBodies returns the command texts that environment
// assignments among tokens make a shell run: PROMPT_COMMAND is a command line
// run before each prompt, PS0/PS1/PS2/PS4 expand command substitutions, and an
// exported function BASH_FUNC_name%%=() { body; } is defined with its body in
// every bash the command starts. Each returned text is to be classified as a
// command of its own.
func envAssignmentCodeBodies(tokens []string) []string {
	var bodies []string
	for _, tok := range tokens {
		key, value, ok := strings.Cut(tok, "=")
		if !ok || value == "" {
			continue
		}
		switch {
		case key == "PROMPT_COMMAND":
			bodies = append(bodies, value)
		case key == "PS0" || key == "PS1" || key == "PS2" || key == "PS4":
			_, subs := extractSubstitutions(value)
			bodies = append(bodies, subs...)
		case strings.HasPrefix(key, "BASH_FUNC_"):
			bodies = append(bodies, strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "()")))
		}
	}
	return bodies
}
