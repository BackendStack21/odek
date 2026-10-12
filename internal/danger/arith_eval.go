package danger

import (
	"regexp"
	"strings"
)

// arithmeticEvalBuiltins evaluate some operands as arithmetic or array
// subscripts. Bash expands a command substitution inside such a subscript
// even when the word was single-quoted on the command line
// (`let 'x[$(cmd)]=1'`), so the substitution extraction, which honours the
// quoting, never sees it.
var arithmeticEvalBuiltins = map[string]bool{
	"let": true, "declare": true, "typeset": true, "local": true,
	"readonly": true, "export": true, "unset": true, "read": true,
	"mapfile": true, "readarray": true, "getopts": true, "printf": true,
	"test": true, "[": true, "[[": true, "((": true,
}

// arithmeticTestOperators make a test/[[ expression evaluate its operands
// arithmetically (-eq …) or as variable names with subscripts (-v, -R).
var arithmeticTestOperators = map[string]bool{
	"-eq": true, "-ne": true, "-lt": true, "-le": true, "-gt": true, "-ge": true,
	"-v": true, "-R": true,
}

// arithmeticOperandSubstitutions returns the bodies of $(...) and `...`
// substitutions found in operands of name that bash evaluates in an
// arithmetic or subscript context, regardless of how the word was quoted.
// Each body is to be classified as a command of its own.
func arithmeticOperandSubstitutions(name string, args []string) []string {
	if !arithmeticEvalBuiltins[name] {
		return nil
	}
	switch name {
	case "test", "[", "[[":
		evaluates := false
		for _, a := range args {
			if arithmeticTestOperators[a] {
				evaluates = true
				break
			}
		}
		if !evaluates {
			return nil
		}
	case "printf":
		// Only the NAME given to -v is a subscripted variable; the format
		// and arguments are data.
		var scoped []string
		for i, a := range args {
			if a == "-v" && i+1 < len(args) {
				scoped = append(scoped, args[i+1])
			} else if strings.HasPrefix(a, "-v") && len(a) > 2 {
				scoped = append(scoped, a[2:])
			}
		}
		args = scoped
	}
	var bodies []string
	for _, a := range args {
		if !strings.Contains(a, "$(") && !strings.Contains(a, "`") {
			continue
		}
		// Quotes inside the already-unquoted operand are data to bash's
		// arithmetic parser; drop them so none can hide a substitution.
		_, subs := extractSubstitutions(strings.ReplaceAll(a, "'", ""))
		bodies = append(bodies, subs...)
	}
	return bodies
}

// matchingBracket returns the index of the `]` closing the `[` at open,
// counting nested brackets. Every scanned byte is charged to budget; ok is
// false for an unterminated bracket or an exhausted budget.
func matchingBracket(s string, open int, budget *int) (int, bool) {
	depth := 0
	for j := open; j < len(s); j++ {
		if *budget--; *budget < 0 {
			return 0, false
		}
		switch s[j] {
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j, true
			}
		}
	}
	return 0, false
}

// arithParamRe matches the start of a parameter expansion body whose offset
// (${name:off:len}) or subscript (${name[sub]}) bash evaluates arithmetically.
// A colon followed by -, =, + or ? is a default/alternative operator instead.
var arithParamRe = regexp.MustCompile(`^[!#]?(?:[A-Za-z_][A-Za-z0-9_]*|[0-9]+|[@*])(?:\[|:[^-=+?])`)

// arithmeticParamBody returns the quote-stripped body of the `${…}` starting
// at i when it is an arithmetic-bearing form (substring or subscript), so the
// caller can extract substitutions bash would run inside it.
func arithmeticParamBody(s string, i int, budget *int) (string, bool) {
	depth := 0
	for j := i + 1; j < len(s); j++ {
		if *budget--; *budget < 0 {
			return "", false
		}
		switch s[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				body := s[i+2 : j]
				if !arithParamRe.MatchString(body) {
					return "", false
				}
				return strings.ReplaceAll(body, "'", ""), true
			}
		}
	}
	return "", false
}
