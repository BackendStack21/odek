package danger

import "strings"

// packageManagerShellKeys are package-manager settings that name a program or
// node options later runs execute: setting one is deferred execution.
var packageManagerShellKeys = fieldSet("script-shell node-options shell editor onload-script git node-gyp " +
	"scriptshell nodeoptions")

// packageManagerSettingClass classifies the settings of npm, pnpm, yarn and
// bun that make later runs execute attacker code. Persisting such a key
// (`npm config set script-shell /tmp/x`, `npm set node-options=…`) is
// persistence; passing --script-shell or --node-options on a run executes the
// operand now (code_execution). Anything else is Safe.
func packageManagerSettingClass(name string, tokens []string) RiskClass {
	switch name {
	case "npm", "pnpm", "yarn", "bun", "npx":
	default:
		return Safe
	}
	cls := Safe
	words := tokens[1:]
	for i, w := range words {
		if w == "--" {
			break
		}
		if strings.HasPrefix(w, "--") {
			key, _, _ := strings.Cut(strings.TrimPrefix(w, "--"), "=")
			if packageManagerShellKeys[strings.ToLower(key)] {
				cls = worstOf(cls, CodeExecution)
			}
			continue
		}
		if w != "set" {
			continue
		}
		// `npm set KEY=VAL ...`, `npm config set [--location L] KEY VAL`,
		// `yarn config set …`: option values and several pairs may precede
		// the key, so every word is checked as a key or KEY=VAL pair.
		for _, arg := range words[i+1:] {
			if strings.HasPrefix(arg, "-") && !strings.Contains(arg, "=") {
				continue
			}
			key, _, _ := strings.Cut(arg, "=")
			if packageManagerShellKeys[strings.ToLower(strings.TrimLeft(key, "-"))] {
				cls = worstOf(cls, Persistence)
			}
		}
	}
	return cls
}
