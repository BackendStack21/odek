package danger

import (
	"strings"
)

// Shell aliases.
//
// sh (dash, macOS /bin/sh, bash in POSIX mode) expands an alias when its name
// is the command word of a simple command read after the definition, also in
// a non-interactive `sh -c` script. A definition alone runs nothing, so it
// keeps the verdict of the alias builtin; a later use is analysed as its
// expansion (the body followed by the remaining words) in addition to the
// literal command. Every body a name was ever given is kept and unalias
// removes nothing, because a definition or removal may sit on a branch that
// never ran: taking the union of all readings can only raise a verdict.

// maxAliasBodies bounds the bodies kept per name and the expansions built for
// one command; past it the command fails closed.
const maxAliasBodies = 16

type aliasTable struct {
	defs map[string][]string
}

func newAliasTable() *aliasTable {
	return &aliasTable{defs: make(map[string][]string)}
}

// aliasExpansion is one reading of an aliased command: the shell text it
// expands to and the alias names expanded on the way.
type aliasExpansion struct {
	text  string
	names []string
}

// validAliasName reports whether s can be the NAME of an alias NAME=VALUE
// definition: non-empty, not an option, and free of characters the shell
// treats as quoting, expansion or separators.
func validAliasName(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.ContainsAny(s, " \t\n/$`'\"\\;&|<>()=")
}

// aliasDefinition is one NAME=VALUE definition with every reading of its value.
type aliasDefinition struct {
	name   string
	bodies []string
}

// aliasDefinitions parses alias arguments into definitions. Option words and
// bare names (which print a definition) define nothing. Normalization folds
// an escaped space outside quotes (`alias x=rm\ -rf\ ~`) into a word break, so
// the words after a definition that are not definitions themselves are also
// read as part of its value: both the short and the joined value are kept.
// ok is false when a definition's name is only known at run time.
func aliasDefinitions(args []string) (defs []aliasDefinition, ok bool) {
	ok = true
	options := true
	for _, a := range args {
		if options && strings.HasPrefix(a, "-") {
			if a == "--" {
				options = false
			}
			continue
		}
		options = false
		name, value, isDef := strings.Cut(a, "=")
		switch {
		case isDef && validAliasName(name):
			defs = append(defs, aliasDefinition{name: name, bodies: []string{value}})
		case isDef && (strings.Contains(name, dynamicSubstToken) || strings.ContainsAny(name, "$`")):
			ok = false
		case len(defs) > 0:
			last := &defs[len(defs)-1]
			joined := last.bodies[len(last.bodies)-1] + " " + a
			last.bodies = append(last.bodies, joined)
		}
	}
	return defs, ok
}

// define records the definitions among alias arguments. It reports false
// when a definition cannot be resolved statically.
func (t *aliasTable) define(args []string) bool {
	defs, ok := aliasDefinitions(args)
	for _, d := range defs {
		for _, body := range d.bodies {
			if !hasString(t.defs[d.name], body) {
				t.defs[d.name] = append(t.defs[d.name], body)
			}
		}
		if len(t.defs[d.name]) > maxAliasBodies {
			ok = false
		}
	}
	return ok
}

func hasString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// lookup returns the bodies of name when it is an alias the shell would
// expand here. skip names the aliases whose expansion text is being read: the
// shell does not expand a word naming an alias it is expanding (a payload that
// is parsed afresh, such as eval or sh -c, is not under that rule).
func (t *aliasTable) lookup(name string, skip []string) []string {
	if t == nil || hasString(skip, name) {
		return nil
	}
	return t.defs[name]
}

// aliasCommandWord returns the index of the word the shell checks for an
// alias: the first word after assignments and the `time` (with its -p / --
// options) and `!` prefixes, after which alias expansion still applies.
func aliasCommandWord(stage []string) int {
	idx := 0
	for idx < len(stage) {
		switch w := stage[idx]; {
		case isAssignment(w), w == "!":
			idx++
		case w == "time":
			idx++
			for idx < len(stage) && strings.HasPrefix(stage[idx], "-") {
				idx++
			}
		default:
			return idx
		}
	}
	return idx
}

// expansions returns the readings of a simple command whose command word (see
// aliasCommandWord) is an alias. A body ending in a blank
// makes the shell check the next word for an alias too. unresolved reports a
// command whose readings exceed the bound.
func (t *aliasTable) expansions(stage, skip []string) (out []aliasExpansion, unresolved bool) {
	if t == nil || len(t.defs) == 0 {
		return nil, false
	}
	idx := aliasCommandWord(stage)
	if idx >= len(stage) || len(t.lookup(stage[idx], skip)) == 0 {
		return nil, false
	}
	prefix := make([]string, 0, idx)
	for _, w := range stage[:idx] {
		prefix = append(prefix, aliasQuoteWord(w))
	}
	type partial struct {
		text  string
		names []string
		next  int // index of the next word to place
		chain bool
	}
	work := []partial{{text: strings.Join(prefix, " "), next: idx, chain: true}}
	for len(work) > 0 {
		p := work[len(work)-1]
		work = work[:len(work)-1]
		var bodies []string
		if p.chain && p.next < len(stage) && !hasString(p.names, stage[p.next]) {
			bodies = t.lookup(stage[p.next], skip)
		}
		if len(bodies) == 0 {
			if len(p.names) == 0 {
				continue
			}
			rest := make([]string, 0, len(stage)-p.next)
			for _, w := range stage[p.next:] {
				rest = append(rest, aliasQuoteWord(w))
			}
			out = append(out, aliasExpansion{text: strings.TrimSpace(p.text + " " + strings.Join(rest, " ")), names: p.names})
			if len(out) > maxAliasBodies {
				return nil, true
			}
			continue
		}
		for _, body := range bodies {
			names := append(append([]string(nil), p.names...), stage[p.next])
			work = append(work, partial{
				text:  p.text + " " + body,
				names: names,
				next:  p.next + 1,
				chain: strings.HasSuffix(body, " ") || strings.HasSuffix(body, "\t"),
			})
			if len(work) > maxAliasBodies*maxAliasBodies {
				return nil, true
			}
		}
	}
	return out, false
}

// aliasQuoteWord renders an already split and expanded word so the shell text
// of an expansion reads it back as the same single word. Operators and
// redirections stay bare so they keep their meaning.
func aliasQuoteWord(w string) string {
	if w == "" {
		return "''"
	}
	if isRedirectToken(w) || isInputOutputRedirect(w) || w == "|" || w == "|&" {
		return w
	}
	plain := true
	for _, r := range w {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-./~:=,+@%^*?[]{}$!#", r)) {
			plain = false
			break
		}
	}
	if plain {
		return w
	}
	return "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
}
