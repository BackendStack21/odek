package danger

import "strings"

// packageManagerShellKeys are package-manager settings that name a program,
// node options, or code that later runs load and execute: setting one is
// deferred execution. Besides the shells and node options this covers yarn
// Berry's yarnPath (the yarn release every run executes) and plugins, npm's
// init-module (run by `npm init`), browser (run by `npm docs`/`repo`), python
// (run by node-gyp builds) and globalconfig/userconfig (a config file that can
// set any of these).
var packageManagerShellKeys = fieldSet("script-shell node-options shell editor onload-script git node-gyp " +
	"scriptshell nodeoptions yarnpath yarn-path plugins init-module init.module initmodule browser python " +
	"globalconfig userconfig")

// packageManagerSettingClass classifies the settings of npm, pnpm, yarn and
// bun that make later runs execute attacker code. Persisting such a key
// (`npm config set script-shell /tmp/x`, `npm set node-options=…`) is
// persistence; passing --script-shell or --node-options on a run executes the
// operand now (code_execution). Installing a yarn plugin (`yarn plugin
// import`) or a yarn release from a URL or path (`yarn set version URL`,
// `yarn policies set-version URL`) is persistence too: every later yarn run
// loads that code. Anything else is Safe.
func packageManagerSettingClass(name string, tokens []string) RiskClass {
	switch name {
	case "npm", "pnpm", "yarn", "bun", "npx":
	default:
		return Safe
	}
	cls := Safe
	words := tokens[1:]
	if name == "yarn" && yarnInstallsLoadedCode(words) {
		cls = Persistence
	}
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

// yarnInstallsLoadedCode reports whether a yarn invocation installs code that
// every later yarn run loads: `yarn plugin import <anything>` (a URL, a path,
// or a package fetched from the registry) and `yarn set version` / `yarn
// policies set-version` given a URL or a path instead of a release name.
func yarnInstallsLoadedCode(words []string) bool {
	var operands []string
	for _, w := range words {
		if w == "--" {
			break
		}
		if !strings.HasPrefix(w, "-") {
			operands = append(operands, w)
		}
	}
	if len(operands) < 2 {
		return false
	}
	switch {
	case operands[0] == "plugin" && operands[1] == "import":
		return true
	case operands[0] == "set" && operands[1] == "version" && len(operands) > 2:
		return yarnReleaseIsLocation(operands[2])
	case operands[0] == "policies" && operands[1] == "set-version" && len(operands) > 2:
		return yarnReleaseIsLocation(operands[2])
	}
	return false
}

// yarnReleaseIsLocation reports whether a `yarn set version` operand is a URL
// or a file path rather than a release name (stable, canary, 4.1.0, from).
func yarnReleaseIsLocation(op string) bool {
	return strings.ContainsAny(op, "/:\\$`") ||
		strings.HasSuffix(op, ".js") || strings.HasSuffix(op, ".cjs") || strings.HasSuffix(op, ".mjs") ||
		strings.Contains(op, dynamicSubstToken)
}
