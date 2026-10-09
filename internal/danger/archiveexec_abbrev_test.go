package danger

import "testing"

// Both tools accept any unambiguous prefix of their long options, so every
// such spelling of the program-running option is code execution; an ambiguous
// prefix names no option and a differently named option is not one.
func TestRED_ArchiveLongOptionAbbreviations(t *testing.T) {
	for _, cmd := range []string{
		"cpio -o --rs=id -F h:a",
		"cpio -o --rs id -F h:a",
		"cpio -o --rsh-c=id -F h:a",
		"cpio -o --rm=id -F h:a",
		"cpio -o --rmt-c id -F h:a",
		"zip out.zip f -T --unzip-comm=id",
		"zip out.zip f -T --unzip-c id",
		"zip out.zip f -T --unz=id",
	} {
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
		}
	}
	for _, cmd := range []string{
		"cpio -o --re -F a.cpio",
		"cpio -o --r -F a.cpio",
		"cpio -o --rename -F a.cpio",
		"cpio -o --reset-access-time -F a.cpio",
		"zip out.zip f --update",
		"zip out.zip f --un",
	} {
		if got := Classify(cmd); Rank(got) >= Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want below code_execution", cmd, got)
		}
	}
}
