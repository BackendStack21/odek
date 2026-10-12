package danger

import "strings"

// defaultExpansion is a parameter expansion with a default or alternative
// word: ${name:-word}, ${name-word}, ${name:=word}, ${name=word},
// ${name:+word} and ${name+word}.
type defaultExpansion struct {
	name  string
	op    string // "-", "=", or "+"
	colon bool   // the form tests for unset-or-empty rather than unset
	word  string
	end   int // index just past the closing brace
}

// parseDefaultExpansion recognises a default/alternative expansion starting
// at token[dollar]. It reports false for every other spelling.
func parseDefaultExpansion(token string, dollar int) (defaultExpansion, bool) {
	var d defaultExpansion
	i := dollar + 2
	if dollar+1 >= len(token) || token[dollar+1] != '{' {
		return d, false
	}
	for i < len(token) && isShellVarByte(token[i]) {
		i++
	}
	d.name = token[dollar+2 : i]
	if d.name == "" || i >= len(token) {
		return d, false
	}
	if token[i] == ':' {
		d.colon = true
		i++
	}
	if i >= len(token) || (token[i] != '-' && token[i] != '=' && token[i] != '+') {
		return d, false
	}
	d.op = string(token[i])
	i++
	depth := 1
	start := i
	for ; i < len(token); i++ {
		switch {
		case token[i] == '$' && i+1 < len(token) && token[i+1] == '{':
			depth++
			i++
		case token[i] == '}':
			depth--
			if depth == 0 {
				d.word = token[start:i]
				d.end = i + 1
				return d, true
			}
		}
	}
	return d, false
}

// resolve returns the text the expansion yields given the variable's static
// value. A variable that is not known is taken as unset, which is the state
// of a variable in a fresh command; the alternative forms of an unknown
// variable stay unresolved. expandWord expands the word part.
func (d defaultExpansion) resolve(value string, known bool, expandWord func(string) string) (string, bool) {
	set := known && (value != "" || !d.colon)
	switch d.op {
	case "-", "=":
		if set {
			return value, true
		}
		return expandWord(d.word), true
	default: // "+"
		if set {
			return expandWord(d.word), true
		}
		if known {
			return "", true
		}
		return "", false
	}
}

// expandDefaultWord resolves a default expansion for expandToken. A resolved
// word that the shell would split or glob cannot be one operand.
func (s *shellAnalysisState) expandDefaultWord(token string, dollar int, separators string) (string, int, bool) {
	if s.work != nil && s.work.literalDefaults {
		return "", 0, false
	}
	d, ok := parseDefaultExpansion(token, dollar)
	if !ok {
		return "", 0, false
	}
	value, known := s.vars[d.name]
	out, ok := d.resolve(value, known, func(w string) string {
		return s.expandToken(w, false, separators)
	})
	if !ok {
		return "", 0, false
	}
	if strings.ContainsAny(out, separators) {
		return dynamicSubstToken, d.end, true
	}
	return out, d.end, true
}
