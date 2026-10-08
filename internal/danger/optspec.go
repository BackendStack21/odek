package danger

import (
	"sort"
	"strings"
)

// optSpec is the option grammar of one command-line tool, described well
// enough to tell options, option values and operands apart. Every classifier
// adapter that needs to find a subcommand, an operand, or the value of an
// option reads its arguments through a spec, so all tools share one reading of
// fused short clusters (`-xvf file`, `-Cdir`), `--opt=value`, `--opt value`,
// unambiguous long-option prefixes, and the `--` terminator.
//
// An option that the spec does not list is a flag without a value: a spec that
// forgets a value-taking option makes the following word an operand, which
// the callers treat as the more cautious reading, while a spec that lists an
// option as taking a value hides the next word from operand inspection. Lists
// therefore name only options the tool really gives a value.
type optSpec struct {
	// short lists the short option letters that take a value: the rest of
	// the cluster when there is one, otherwise the next word.
	short string
	// shortOptional lists short option letters whose value, when there is
	// one, is only the rest of the word (`xargs -iFOO`, `sed -i.bak`): they
	// never take the next word.
	shortOptional string
	// exact lists multi-letter single-dash options that take a value (`-arch
	// x86_64`, `-fprint file`), also spelled `-name=value`.
	exact []string
	// long maps each long option name to whether it takes a value.
	long map[string]bool
	// alias maps a short letter to the long option it stands for, so a caller
	// can match one canonical name for either spelling.
	alias map[byte]string
	// abbrev accepts any unambiguous prefix of a listed long option, as GNU
	// getopt_long and curl do. Tools whose parsers are exact (git, pflag
	// based CLIs) leave it unset. An ambiguous prefix resolves to every
	// option it could name.
	abbrev bool
	// minAbbrev is the shortest prefix, in characters after `--`, that is
	// accepted as an abbreviation (default 1).
	minAbbrev int
	// foldLong lower-cases long option names before the lookup, for tools
	// whose flag names are case-insensitive.
	foldLong bool
	// shortEq drops a leading `=` from a short option's fused value
	// (`-n=5`), as pflag does.
	shortEq bool
	// posix ends option parsing at the first operand: everything from there
	// on is an operand, so options cannot follow operands. It is how tools
	// that take a subcommand or a wrapped command read their arguments.
	posix bool
	// operandLimit, when positive, ends option parsing at the first operand
	// beyond that count (ssh: the host is operand one, the remote command
	// starts at operand two).
	operandLimit int
	// ignoreDashDash reads `--` as an ordinary word instead of the end of
	// the options. Predicates that look for an option which makes a tool run
	// a program use it, so a `--` cannot hide a later option from them.
	ignoreDashDash bool
}

// optArg is one option read from the arguments.
type optArg struct {
	// names are the canonical spellings the word can stand for: "-x" for a
	// short option without an alias, "--name" for a long one (or the alias
	// of a short one). An ambiguous abbreviation lists every candidate.
	names []string
	// value is the option's value, when has is set.
	value string
	// takes reports that the option is one that takes a value, whether or not
	// the arguments supplied it.
	takes bool
	// has reports that a value was supplied: fused, after `=`, or as the
	// next word. A value-taking option at the end of the arguments has none.
	has bool
	// at is the index of the word that spells the option and end the index
	// of the last word it consumed (the value word, when it is separate).
	at, end int
}

// is reports whether the option can be any of the named spellings.
func (o optArg) is(names ...string) bool {
	for _, n := range o.names {
		for _, want := range names {
			if n == want {
				return true
			}
		}
	}
	return false
}

// unique reports whether the option names exactly one option, that is, whether
// it is not an ambiguous abbreviation.
func (o optArg) unique() bool { return len(o.names) == 1 }

// optResult is the outcome of reading a whole argument list.
type optResult struct {
	opts []optArg
	// operands are the non-option words, in order. When the spec ends option
	// parsing early (posix, operandLimit) they include the unparsed tail.
	operands []string
	// rest are the words after the `--` terminator.
	rest []string
	// operandAt is the index of the first operand in the argument list, or -1.
	operandAt int
}

// args returns the operands followed by the words after `--`: every word that
// is not an option or an option value.
func (r optResult) args() []string {
	if len(r.rest) == 0 {
		return r.operands
	}
	return append(append([]string(nil), r.operands...), r.rest...)
}

// has reports whether any of the named options was given.
func (r optResult) has(names ...string) bool {
	for _, o := range r.opts {
		if o.is(names...) {
			return true
		}
	}
	return false
}

// values returns the values supplied for any of the named options.
func (r optResult) values(names ...string) []string {
	var out []string
	for _, o := range r.opts {
		if o.has && o.is(names...) {
			out = append(out, o.value)
		}
	}
	return out
}

// valueOpts builds a long-option table of options that all take a value.
func valueOpts(names string) map[string]bool { return longTable(names, "") }

// fieldSet turns a space-separated list into a set.
func fieldSet(s string) map[string]bool {
	out := make(map[string]bool)
	for _, f := range strings.Fields(s) {
		out[f] = true
	}
	return out
}

// longTable builds a long-option table from space-separated lists of options
// that take a value and options that do not.
func longTable(withValue, flags string) map[string]bool {
	out := make(map[string]bool)
	for _, f := range strings.Fields(flags) {
		out[f] = false
	}
	for _, f := range strings.Fields(withValue) {
		out[f] = true
	}
	return out
}

// resolveLong maps the name of a long option, as typed, to the options it can
// stand for and whether any of them takes a value.
func (s optSpec) resolveLong(name string) (names []string, takes bool) {
	if s.foldLong {
		name = strings.ToLower(name)
	}
	if t, ok := s.long[name]; ok {
		return []string{"--" + name}, t
	}
	if s.abbrev && name != "" && len(name) >= s.minAbbrev {
		for known, t := range s.long {
			if strings.HasPrefix(known, name) {
				names = append(names, "--"+known)
				takes = takes || t
			}
		}
		if len(names) > 0 {
			sort.Strings(names)
			return names, takes
		}
	}
	return []string{"--" + name}, false
}

// option reads the option that starts at args[i], a word beginning with a dash
// that is neither `-` nor `--`, and returns what it names and the index of the
// next unread word. A short cluster yields one entry per letter up to and
// including the first value-taking one, which takes the rest of the word (or
// the next word) as its value.
func (s optSpec) option(args []string, i int) (opts []optArg, next int) {
	tok := args[i]
	if len(tok) < 2 || tok[0] != '-' {
		return nil, i + 1
	}
	// take reads a value that is either fused into the word or the next word.
	take := func(o optArg, fused string, hasFused bool) (optArg, int) {
		switch {
		case hasFused:
			o.value, o.has, o.end = fused, true, i
			return o, i + 1
		case i+1 < len(args):
			o.value, o.has, o.end = args[i+1], true, i+1
			return o, i + 2
		}
		o.end = i
		return o, i + 1
	}
	if tok[1] == '-' {
		if tok == "--" {
			return nil, i + 1
		}
		name, val, hasEq := strings.Cut(tok[2:], "=")
		names, takes := s.resolveLong(name)
		o := optArg{names: names, takes: takes, at: i, end: i}
		switch {
		case hasEq:
			o.value, o.has = val, true
			return []optArg{o}, i + 1
		case takes:
			o, next = take(o, "", false)
			return []optArg{o}, next
		}
		return []optArg{o}, i + 1
	}
	for _, ex := range s.exact {
		if tok == ex {
			o, next := take(optArg{names: []string{ex}, takes: true, at: i}, "", false)
			return []optArg{o}, next
		}
		if v, ok := strings.CutPrefix(tok, ex+"="); ok {
			return []optArg{{names: []string{ex}, takes: true, value: v, has: true, at: i, end: i}}, i + 1
		}
	}
	for j := 1; j < len(tok); j++ {
		c := tok[j]
		name := "-" + string(c)
		if long, ok := s.alias[c]; ok {
			name = long
		}
		o := optArg{names: []string{name}, at: i, end: i}
		if strings.IndexByte(s.shortOptional, c) >= 0 {
			if fused := tok[j+1:]; fused != "" {
				o.value, o.has = fused, true
			}
			o.takes = true
			return append(opts, o), i + 1
		}
		if strings.IndexByte(s.short, c) >= 0 {
			o.takes = true
			fused := tok[j+1:]
			hasFused := fused != ""
			if s.shortEq && strings.HasPrefix(fused, "=") {
				fused, hasFused = fused[1:], true
			}
			o, next = take(o, fused, hasFused)
			return append(opts, o), next
		}
		opts = append(opts, o)
	}
	return opts, i + 1
}

// valueOption reads the option at args[i] and returns its value-taking part:
// the option that consumed a value (or would have, at the end of the
// arguments). ok is false for a word that is not an option or whose options
// take no value. next is the index of the next unread word either way.
func (s optSpec) valueOption(args []string, i int) (opt optArg, next int, ok bool) {
	opts, next := s.option(args, i)
	for _, o := range opts {
		if o.takes {
			return o, next, true
		}
	}
	return optArg{}, next, false
}

// parse reads a whole argument list (the words after the program name, with
// redirections already removed) into options, operands and the words after
// `--`.
func (s optSpec) parse(args []string) optResult {
	r := optResult{operandAt: -1}
	for i := 0; i < len(args); {
		tok := args[i]
		switch {
		case tok == "--" && !s.ignoreDashDash:
			r.rest = args[i+1 : len(args) : len(args)]
			return r
		case len(tok) > 1 && tok[0] == '-':
			opts, next := s.option(args, i)
			r.opts = append(r.opts, opts...)
			i = next
		default:
			if r.operandAt < 0 {
				r.operandAt = i
			}
			if s.posix || (s.operandLimit > 0 && len(r.operands) >= s.operandLimit) {
				if len(r.operands) == 0 {
					r.operands = args[i:len(args):len(args)]
				} else {
					r.operands = append(r.operands, args[i:]...)
				}
				return r
			}
			r.operands = append(r.operands, tok)
			i++
		}
	}
	return r
}

// valueFlags lists every spelling of the options that take a value ("-n",
// "--namespace") for callers that compare whole words.
func (s optSpec) valueFlags() map[string]bool {
	out := make(map[string]bool)
	for _, c := range s.short {
		out["-"+string(c)] = true
	}
	for _, ex := range s.exact {
		out[ex] = true
	}
	for name, takes := range s.long {
		if takes {
			out["--"+name] = true
		}
	}
	return out
}
