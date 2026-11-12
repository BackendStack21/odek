package danger

import "strings"

// Command-table bindings.
//
// `hash -p FILE NAME` (bash, macOS /bin/sh) and zsh's `hash NAME=FILE` bind
// NAME in the shell's command table: every later NAME used as a command word
// runs FILE. That is a rebinding exactly like an alias, so the binding is
// recorded in the alias table and a later use is analysed as FILE followed by
// the remaining words, alongside the literal command. A FILE only known at
// run time binds to a body that classifies as unresolved, and a NAME only
// known at run time fails the command closed.

// hashUnresolvedBody is the alias body recorded for a binding whose file is
// computed at run time: a command word that cannot be resolved statically.
const hashUnresolvedBody = "\"$odek_unresolved_hash_target\""

// hashBindings returns the alias-table definitions (NAME=BODY) made by the
// arguments of a `hash` invocation. ok is false when a bound name is only
// known at run time.
func hashBindings(args []string) (defs []string, ok bool) {
	ok = true
	var files, names []string
	pending := false // the next word is the argument of -p
	options := true
	for _, a := range args {
		if pending {
			files = append(files, a)
			pending = false
			continue
		}
		if options && len(a) > 1 && a[0] == '-' {
			if a == "--" {
				options = false
				continue
			}
			if i := strings.IndexByte(a, 'p'); i > 0 {
				if rest := a[i+1:]; rest != "" {
					files = append(files, rest)
				} else {
					pending = true
				}
			}
			continue
		}
		options = false
		if name, file, bound := strings.Cut(a, "="); bound && name != "" {
			if !validAliasName(name) {
				ok = false
				continue
			}
			defs = append(defs, name+"="+hashBody(file))
			continue
		}
		names = append(names, a)
	}
	if len(files) == 0 {
		return defs, ok
	}
	for _, name := range names {
		if !validAliasName(name) {
			ok = false
			continue
		}
		for _, file := range files {
			defs = append(defs, name+"="+hashBody(file))
		}
	}
	return defs, ok
}

// bindHash records the bindings made by the arguments of a `hash` invocation.
// It reports false when a binding cannot be resolved statically.
func (t *aliasTable) bindHash(args []string) bool {
	defs, ok := hashBindings(args)
	for _, d := range defs {
		name, _, _ := strings.Cut(d, "=")
		t.hashed[name] = true
	}
	return t.define(defs) && ok
}

// hashBody renders the file a name is bound to as the body of an alias.
func hashBody(file string) string {
	if file == "" || strings.Contains(file, dynamicSubstToken) || strings.ContainsAny(file, "$`") {
		return hashUnresolvedBody
	}
	return aliasQuoteWord(file)
}
