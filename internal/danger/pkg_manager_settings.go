package danger

import "strings"

// packageManagerShellKeys are package-manager settings that name a program or
// node options later runs execute: setting one is deferred execution.
var packageManagerShellKeys = fieldSet("script-shell node-options shell editor onload-script git node-gyp " +
	"scriptshell nodeoptions")

// packageManagerLoaderKeys are package-manager settings that name code the
// package manager itself loads later: yarn Berry's yarnPath (the yarn release
// every run executes) and plugins, npm's init-module (run by `npm init`),
// browser (run by `npm docs`/`repo`), python (run by node-gyp builds) and
// globalconfig/userconfig (a config file that can set any of these). Bins run
// through a package manager commonly take options with the same names
// (`yarn playwright test --browser=chromium`), so outside npm they count only
// as the package manager's own options.
var packageManagerLoaderKeys = fieldSet("yarnpath yarn-path plugins init-module init.module initmodule " +
	"browser python globalconfig userconfig")

// packageManagerOwnCommands are the built-in commands whose options are the
// package manager's own rather than those of a bin or script it runs.
var packageManagerOwnCommands = fieldSet("install i add ci update up upgrade remove rm uninstall un link unlink " +
	"rebuild rb init create config set dedupe prune pack publish audit outdated docs repo home bugs import")

// packageManagerSettingClass classifies the settings of npm, pnpm, yarn and
// bun that make later runs execute attacker code. Persisting such a key
// (`npm config set script-shell /tmp/x`, `npm set node-options=…`) is
// persistence; passing one as an option on a run executes the operand now
// (code_execution). Installing a yarn plugin (`yarn plugin import`) or a yarn
// release from a URL or path (`yarn set version URL`, `yarn policies
// set-version URL`) is persistence too: every later yarn run loads that code.
// Anything else is Safe.
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
	// npm reads every option before `--` as configuration, whatever command
	// it runs; the others hand options after a bin or script to that program.
	ownOptionsEverywhere := name == "npm"
	if !ownOptionsEverywhere {
		for _, w := range words {
			if w == "--" {
				break
			}
			if !strings.HasPrefix(w, "-") {
				ownOptionsEverywhere = packageManagerOwnCommands[w]
				break
			}
		}
	}
	seenOperand := false
	for i, w := range words {
		if w == "--" {
			break
		}
		if strings.HasPrefix(w, "--") {
			key, _, _ := strings.Cut(strings.TrimPrefix(w, "--"), "=")
			key = strings.ToLower(key)
			if packageManagerShellKeys[key] ||
				(packageManagerLoaderKeys[key] && (ownOptionsEverywhere || !seenOperand)) {
				cls = worstOf(cls, CodeExecution)
			}
			continue
		}
		if !strings.HasPrefix(w, "-") {
			seenOperand = true
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
			key = strings.ToLower(strings.TrimLeft(key, "-"))
			if packageManagerShellKeys[key] || packageManagerLoaderKeys[key] {
				cls = worstOf(cls, Persistence)
			}
		}
	}
	return cls
}

// yarnValueOptions are yarn's global options that take a separate value.
var yarnValueOptions = fieldSet("--cwd --cache-folder --modules-folder --mutex --network-timeout --global-folder " +
	"--link-folder --registry --use-yarnrc --preferred-cache-folder --network-concurrency --proxy --https-proxy " +
	"--otp")

// yarnInstallsLoadedCode reports whether a yarn invocation installs code that
// every later yarn run loads: `yarn plugin import <anything>` / `yarn plugin
// import-from-sources` (a URL, a path, or a package fetched from the
// registry) and `yarn set version` / `yarn policies set-version` given a URL
// or a path instead of a release name. Leading global options (and their
// values) and a `workspace NAME` prefix are skipped; after a leading option
// whose arity is unknown the command is judged both with and without the
// word that follows it.
func yarnInstallsLoadedCode(words []string) bool {
	i := 0
	ambiguous := false
	for i < len(words) && strings.HasPrefix(words[i], "-") && words[i] != "--" {
		w := words[i]
		i++
		if strings.Contains(w, "=") || !strings.HasPrefix(w, "--") {
			continue
		}
		if yarnValueOptions[w] {
			i++
		} else {
			ambiguous = true
		}
	}
	if i > len(words) {
		return false
	}
	candidates := [][]string{words[i:]}
	if ambiguous && i < len(words) {
		candidates = append(candidates, words[i+1:])
	}
	for _, rest := range candidates {
		if yarnCommandInstallsLoadedCode(rest) {
			return true
		}
	}
	return false
}

// yarnCommandInstallsLoadedCode judges the words from yarn's subcommand on.
func yarnCommandInstallsLoadedCode(rest []string) bool {
	var operands []string
	for _, w := range rest {
		if w == "--" {
			break
		}
		if !strings.HasPrefix(w, "-") {
			operands = append(operands, w)
		}
	}
	if len(operands) >= 2 && operands[0] == "workspace" {
		operands = operands[2:]
	}
	if len(operands) < 2 {
		return false
	}
	switch {
	case operands[0] == "plugin" && (operands[1] == "import" || operands[1] == "import-from-sources"):
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
