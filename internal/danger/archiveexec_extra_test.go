package danger

import "testing"

func TestArchiveToolProgramOptionSpellings(t *testing.T) {
	for _, cmd := range []string{
		"zip out.zip f -T -TTid",
		"zip out.zip f -T --unzip-command id",
		"zip out.zip f -T --unzip-command=id",
		"cpio -o --rsh=id -F h:a",
		"cpio -o --rmt-command id -F h:a",
		"cpio -o --rmt=id -F h:a",
	} {
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
		}
	}
	for _, cmd := range []string{
		"zip out.zip f",
		"zip -T out.zip f",
		"zip -r out.zip dir",
		"cpio -o -F archive.cpio",
		"cpio -o --format=newc -F a.cpio",
		"cpio --re -o -F a.cpio",
	} {
		if got := Classify(cmd); Rank(got) >= Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want below code_execution", cmd, got)
		}
	}
	if zipRunsCommand([]string{"zip", "--", "-TT"}) || cpioRunsCommand([]string{"cpio", "--", "--rsh-command=x"}) {
		t.Error("nothing after -- is an option")
	}
}
