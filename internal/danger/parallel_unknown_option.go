package danger

import "strings"

// parallelKnownShort are the short options GNU parallel documents, with and
// without a value.
const parallelKnownShort = "aCdEIjJLnNPsSielkXmugqvxrpt0TH"

// parallelHasUnknownOption reports whether the options a GNU parallel
// invocation in the stage carries before its command include one the option
// table does not know. Such an option could take a value, and reading it as a
// flag would make that value the command, hiding the real one: the whole
// command is then unclassifiable.
func parallelHasUnknownOption(stage []string) bool {
	idx := argvComposerIndex(stage)
	if idx < 0 || commandName(stage[idx]) != "parallel" {
		return false
	}
	inner, ok := argvComposerInnerCommand(stage)
	if !ok || len(stage)-len(inner) <= idx+1 {
		return false
	}
	opts := stage[idx+1 : len(stage)-len(inner)]
	for i := 0; i < len(opts); {
		tok := opts[i]
		if len(tok) < 2 || tok[0] != '-' || tok == "--" {
			i++
			continue
		}
		parsed, next := parallelGrammar.option(opts, i)
		for _, o := range parsed {
			for _, n := range o.names {
				if strings.HasPrefix(n, "--") {
					if _, known := parallelGrammar.long[n[2:]]; !known {
						return true
					}
				} else if len(n) == 2 && !strings.ContainsRune(parallelKnownShort, rune(n[1])) {
					return true
				}
			}
		}
		i = next
	}
	return false
}
