package memory

import "testing"

// FactLooksUnsafe skips the substitution expressions when the text has no (
// or backtick, and the unquoted reading when it has no ', " or \; every
// substitution expression needs a ( or a backtick, and removing nothing
// changes nothing, so the verdicts are those of running every check on both
// readings.
func TestFactLooksUnsafeGatesMatchUngated(t *testing.T) {
	ungated := func(fact string) bool {
		if releasePipeRe.MatchString(blankBareTableRows(fact)) || releaseEvalRe.MatchString(fact) {
			return true
		}
		joined := lineContinuations.Replace(fact)
		for _, text := range []string{joined, unquoted.Replace(joined)} {
			for _, re := range substitutionFetchRes {
				if re.MatchString(text) {
					return true
				}
			}
			scan := blankBareTableRows(text)
			if pipelineRunsFetch(scan) || powerShellRunsFetch(scan) {
				return true
			}
		}
		return false
	}
	pipes := rh1112dPipeCorpus()
	corpus := append(append(rh1112dPackageLiterals(t), rh1112dSubstitutionCorpus()...), rh1112dOrdinaryMentions...)
	for i := 0; i < len(pipes); i += 16 {
		corpus = append(corpus, pipes[i])
	}
	for _, s := range corpus {
		if got, want := FactLooksUnsafe(s), ungated(s); got != want {
			t.Errorf("FactLooksUnsafe(%q) = %v, all checks = %v", s, got, want)
		}
	}
}
