package danger

import "strings"

// Command-table bindings.
//
// `hash -p FILE NAME` (bash, macOS /bin/sh) and zsh's `hash NAME=FILE` bind
// NAME in the shell's command table: every later NAME used as a command word
// runs FILE. That is a rebinding exactly like an alias, so the binding is
// recorded in the alias table and a later use is analysed as FILE followed by
// the remaining words, alongside the literal command. The lookup builtins
// (`exec`, `command`, `builtin command`) consult the same table for their
// operand, so a NAME behind any chain of them resolves the binding too. A
// FILE only known at run time binds to a body that classifies as unresolved,
// and a NAME only known at run time fails the command closed.

// hashUnresolvedBody is the alias body recorded for a binding whose file is
// computed at run time: a command word that cannot be resolved statically.
const hashUnresolvedBody = "\"$odek_unresolved_hash_target\""

// hashBindings returns the alias-table definitions (NAME=BODY) made by the
// arguments of a `hash` invocation. bash binds every NAME to the file of the
// last -p (an earlier -p is overridden), so each name gets one definition.
// ok is false when a bound name is only known at run time.
func hashBindings(args []string) (defs []string, ok bool) {
	ok = true
	var names []string
	file, hasFile := "", false
	pending := false // the next word is the argument of -p
	options := true
	for _, a := range args {
		if pending {
			file, hasFile = a, true
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
					file, hasFile = rest, true
				} else {
					pending = true
				}
			}
			continue
		}
		options = false
		if name, f, bound := strings.Cut(a, "="); bound && name != "" {
			if !validAliasName(name) {
				ok = false
				continue
			}
			defs = append(defs, name+"="+hashBody(f))
			continue
		}
		names = append(names, a)
	}
	if !hasFile {
		return defs, ok
	}
	body := hashBody(file)
	for _, name := range names {
		if !validAliasName(name) {
			ok = false
			continue
		}
		defs = append(defs, name+"="+body)
	}
	return defs, ok
}

// bindHash records the bindings made by the arguments of a `hash` invocation.
// It reports false when a binding cannot be resolved statically.
func (t *aliasTable) bindHash(args []string) bool {
	defs, ok := hashBindings(args)
	return t.bindTable(defs) && ok
}

// bindTable records command-table definitions (NAME=BODY).
func (t *aliasTable) bindTable(defs []string) bool {
	for _, d := range defs {
		name, _, _ := strings.Cut(d, "=")
		t.hashed[name] = true
	}
	return t.define(defs)
}

// bashCmdsVar is bash's view of its command table (bash 4+): "elements added
// to this array appear in the hash table", so BASH_CMDS[NAME]=FILE binds NAME
// exactly like `hash -p FILE NAME`.
const bashCmdsVar = "BASH_CMDS"

// bindBashCmds records the command-table bindings one simple command makes
// through BASH_CMDS: an assignment (`BASH_CMDS=([ls]=/bin/rm)`,
// `BASH_CMDS[ls]=/bin/rm`, `+=` forms), a declaration operand (`declare -A
// BASH_CMDS=(...)`, `declare BASH_CMDS[ls]=...`), and the builtins that store
// input into an element (`printf -v 'BASH_CMDS[ls]'`, `read 'BASH_CMDS[ls]'`),
// whose file is only known at run time. It reports false when a binding cannot
// be resolved statically: a computed name, a whole-array read (`read -a`,
// `mapfile`), or a nameref to the array.
func (t *aliasTable) bindBashCmds(stage, inner []string) bool {
	if t == nil || !strings.Contains(strings.Join(stage, " "), bashCmdsVar) {
		return true
	}
	var words []string
	if len(inner) == 0 {
		words = stage
	} else {
		name := commandName(inner[0])
		switch name {
		case "declare", "typeset", "local", "export", "readonly":
			nameref := false
			for _, a := range inner[1:] {
				if len(a) > 1 && a[0] == '-' && strings.ContainsRune(a[1:], 'n') && name != "export" {
					nameref = true
				}
			}
			if nameref {
				// A later assignment through the nameref cannot be followed.
				for _, a := range inner[1:] {
					if strings.Contains(a, bashCmdsVar) {
						return false
					}
				}
				return true
			}
			words = inner[1:]
		case "printf", "read", "mapfile", "readarray":
			r := bindingBuiltinSpecs[name].parse(inner[1:])
			var targets []string
			for _, o := range r.opts {
				// printf -v NAME and read -a NAME store into the variable
				// the option names.
				if o.has && ((name == "printf" && o.is("-v")) || (name == "read" && o.is("-a"))) {
					targets = append(targets, o.value)
				}
			}
			if name != "printf" {
				targets = append(targets, r.args()...)
			}
			var defs []string
			for _, target := range targets {
				rest, ok := strings.CutPrefix(target, bashCmdsVar)
				if !ok {
					continue
				}
				key, isElem := bashCmdsSubscript(rest)
				if !isElem || computedWord(key) {
					return false // the whole array, or a computed element
				}
				if validAliasName(key) {
					defs = append(defs, key+"="+hashUnresolvedBody)
				}
			}
			return t.bindTable(defs)
		case "let", "getopts":
			// An element assigned by arithmetic, or filled from the options
			// getopts parses, holds a file the analysis does not follow.
			for _, a := range inner[1:] {
				if strings.Contains(a, bashCmdsVar) {
					return false
				}
			}
			return true
		default:
			// BASH_CMDS[NAME]=FILE and BASH_CMDS+=(...) are not read as
			// assignment words, so they reach here as the command word; an
			// array literal with a space after its parenthesis
			// (`BASH_CMDS=( [ls]=/bin/rm )`) leaves `BASH_CMDS=(` among the
			// assignment words and its elements as the command.
			switch {
			case strings.HasPrefix(inner[0], bashCmdsVar+"["), strings.HasPrefix(inner[0], bashCmdsVar+"+="):
				words = inner
			case bashCmdsLiteralOpens(stage[:len(stage)-len(inner)]):
				words = stage
			default:
				return true
			}
		}
	}
	defs, ok := bashCmdsDefinitions(words)
	return t.bindTable(defs) && ok
}

// bashCmdsSubscript reads `[KEY]` at the start of rest and returns KEY; it
// reports false when rest is not a single element.
func bashCmdsSubscript(rest string) (string, bool) {
	if !strings.HasPrefix(rest, "[") {
		return "", false
	}
	end := strings.IndexByte(rest, ']')
	if end < 0 || end != len(rest)-1 {
		return "", false
	}
	return rest[1:end], true
}

// bashCmdsLiteralOpens reports whether one of the assignment words opens an
// array literal of BASH_CMDS that the word itself does not close.
func bashCmdsLiteralOpens(assignments []string) bool {
	for _, w := range assignments {
		rest, named := strings.CutPrefix(w, bashCmdsVar)
		if named && (strings.HasPrefix(rest, "=(") || strings.HasPrefix(rest, "+=(")) && !strings.HasSuffix(rest, ")") {
			return true
		}
	}
	return false
}

// bashCmdsDefinitions returns the command-table definitions the assignment
// words among words make to BASH_CMDS, following an array literal across the
// words the tokenizer split it into. A literal lists [NAME]=FILE elements or,
// with no subscript at all, alternating names and files (bash 5.1+). ok is
// false when an element's name is only known at run time, a literal mixes the
// two forms or names a key without a file, or the literal never closes.
func bashCmdsDefinitions(words []string) (defs []string, ok bool) {
	ok = true
	element := func(e string) {
		end := strings.IndexByte(e, ']')
		if end < 0 {
			ok = false
			return
		}
		key, rest := e[1:end], e[end+1:]
		value, assigned := strings.CutPrefix(strings.TrimPrefix(rest, "+"), "=")
		switch {
		case !assigned:
			ok = false
		case computedWord(key):
			ok = false
		case validAliasName(key):
			defs = append(defs, key+"="+hashBody(value))
		}
	}
	// literal collects the elements of the array literal being read.
	var literal []string
	closeLiteral := func() {
		var plain []string
		for _, e := range literal {
			if strings.HasPrefix(e, "[") {
				element(e)
			} else {
				plain = append(plain, e)
			}
		}
		if len(plain) > 0 && (len(plain) != len(literal) || len(plain)%2 != 0) {
			ok = false
		} else {
			for k := 0; k+1 < len(plain); k += 2 {
				key := plain[k]
				switch {
				case computedWord(key):
					ok = false
				case validAliasName(key):
					defs = append(defs, key+"="+hashBody(plain[k+1]))
				}
			}
		}
		literal = nil
	}
	add := func(body string) {
		if body = strings.TrimSpace(body); body != "" {
			literal = append(literal, body)
		}
	}
	inLiteral := false
	for _, w := range words {
		if inLiteral {
			body, closed := strings.CutSuffix(w, ")")
			add(body)
			if inLiteral = !closed; closed {
				closeLiteral()
			}
			continue
		}
		rest, named := strings.CutPrefix(w, bashCmdsVar)
		if !named {
			continue
		}
		switch {
		case strings.HasPrefix(rest, "["):
			// BASH_CMDS[NAME]=FILE or BASH_CMDS[NAME]+=FILE.
			element(rest)
		case strings.HasPrefix(rest, "=(") || strings.HasPrefix(rest, "+=("):
			body := rest[strings.IndexByte(rest, '(')+1:]
			body, closed := strings.CutSuffix(body, ")")
			add(body)
			if inLiteral = !closed; closed {
				closeLiteral()
			}
		}
	}
	if inLiteral {
		// The elements read so far still bind.
		closeLiteral()
		ok = false
	}
	return defs, ok
}

// shellLookupBuiltins are the builtins that run their operand through the
// shell's own command lookup, which reads the command table before PATH,
// with their option grammars: `command [-p]`, `exec [-cl] [-a NAME]`, and
// `builtin`, whose operand is run only when it names a builtin (so only a
// lookup builtin there continues the chain). External wrappers (env, nohup,
// nice, xargs, sudo, ...) search PATH on their own and never see the table.
var shellLookupBuiltins = map[string]optSpec{
	"command": {posix: true},
	"exec":    {posix: true, short: "a"},
	"builtin": {posix: true},
}

// tableLookupWords returns the indexes of the words of a simple command that
// the shell looks up as commands, in order: the command word (after
// assignments, `!` and `time`; see aliasCommandWord) and the operand of every
// lookup builtin chained behind it (`exec NAME`, `command exec NAME`,
// `builtin command exec -c NAME`). A `command -v`/`-V` query runs nothing and
// ends the chain.
func tableLookupWords(stage []string) []int {
	idx := aliasCommandWord(stage)
	var out []int
	viaBuiltin := false
	for idx < len(stage) {
		name := commandName(stage[idx])
		spec, lookup := shellLookupBuiltins[name]
		if viaBuiltin && !lookup {
			break // `builtin NAME` runs only builtins
		}
		out = append(out, idx)
		if !lookup {
			break
		}
		args := stage[idx+1:]
		if name == "command" && commandIsLookup(args) {
			break
		}
		r := spec.parse(args)
		switch {
		case r.operandAt >= 0:
			idx += 1 + r.operandAt
		case len(r.rest) > 0:
			idx = len(stage) - len(r.rest)
		default:
			return out
		}
		viaBuiltin = name == "builtin"
	}
	return out
}

// hashBody renders the file a name is bound to as the body of an alias.
func hashBody(file string) string {
	if file == "" || strings.Contains(file, dynamicSubstToken) || strings.ContainsAny(file, "$`") {
		return hashUnresolvedBody
	}
	return aliasQuoteWord(file)
}
