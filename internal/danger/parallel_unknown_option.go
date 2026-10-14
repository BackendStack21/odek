package danger

import (
	"os"
	"strings"
)

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

// parallelProgramOptions take a command (or perl expression, or ssh login
// command) that parallel runs; parallelWriteOptions name files or directories
// parallel creates, writes or transfers into.
var (
	parallelProgramOptions = []string{"--compress-program", "--decompress-program", "--rpl", "--sshlogin", "-S"}
	parallelWriteOptions   = []string{"--results", "--res", "--joblog", "--return", "--basefile", "--bf",
		"--tmpdir", "--workdir", "--wd", "--transfer-file", "--transferfile", "--tf"}
)

// parallelOptionValueRisk classifies what the value-taking options of a GNU
// parallel invocation do: a program-valued option runs its value (a command
// line judged like any other, never lower than code_execution) and a
// path-valued one writes there, judged by the write rules for that path.
func parallelOptionValueRisk(stage []string) RiskClass {
	idx := argvComposerIndex(stage)
	if idx < 0 || commandName(stage[idx]) != "parallel" {
		return Safe
	}
	inner, ok := argvComposerInnerCommand(stage)
	if !ok || len(stage)-len(inner) <= idx+1 {
		return Safe
	}
	opts := stage[idx+1 : len(stage)-len(inner)]
	cwd, _ := os.Getwd()
	state := shellAnalysisState{cwd: cwd}
	cls := Safe
	for i := 0; i < len(opts); {
		if len(opts[i]) < 2 || opts[i][0] != '-' || opts[i] == "--" {
			i++
			continue
		}
		parsed, next := parallelGrammar.option(opts, i)
		for _, o := range parsed {
			if !o.has || o.value == "" {
				continue
			}
			switch {
			case o.is(parallelProgramOptions...):
				cls = worstOf(cls, CodeExecution)
				if v := Classify(o.value); v != Unknown {
					cls = worstOf(cls, v)
				}
			case o.is(parallelWriteOptions...):
				v := state.targetRiskMode(o.value, cwd, cwd != "", true)
				cls = worstOf(cls, v)
				if Rank(v) >= Rank(SystemWrite) {
					// A sensitive or system target was denied outright when
					// the option's value used to read as the command; keep it.
					cls = worstOf(cls, Unknown)
				}
			}
		}
		i = next
	}
	return cls
}
