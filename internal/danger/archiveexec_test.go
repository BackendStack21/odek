package danger

import "testing"

// Archive tools are classified as plain local writes ("tar --to-command / -I
// escalate"), but zip's test-command option and cpio's remote-shell option
// also execute an arbitrary program. They must classify as code_execution
// like the tar equivalents.
func TestRED_ArchiveToolsProgramOptionsAreCodeExecution(t *testing.T) {
	for _, cmd := range []string{
		"zip out.zip file.txt -T -TT 'sh -c id #'",
		"zip -T -TT id out.zip file.txt",
		"cpio -o --rsh-command=id -F host:archive.cpio",
		"cpio -i --rsh-command id -F host:archive.cpio",
	} {
		if got := Classify(cmd); got != CodeExecution {
			t.Errorf("Classify(%q) = %s, want code_execution", cmd, got)
		}
	}
}
