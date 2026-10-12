package redact

import "testing"

func TestRedactSecrets_WorkingDirectoriesAndTestNamesSurvive(t *testing.T) {
	for _, in := range []string{
		"PWD=/home/user/projects/odek2026/src",
		"pwd: /Users/kyberneees/Work/github/21no.de/odek",
		"pwd=~/projects/odek2026/some/long/dir",
		"pass: TestHandleSessionByID_Delete2",
		"token: ./relative/path/with/digits123/file",
	} {
		if got := RedactSecrets(in); got != in {
			t.Errorf("RedactSecrets(%q) = %q, want unchanged", in, got)
		}
	}
	if got := RedactSecrets("MYSQL_PWD=aB3dEf7hIj9kLmN0pQrS"); got == "MYSQL_PWD=aB3dEf7hIj9kLmN0pQrS" {
		t.Errorf("prefixed _PWD key must still be redacted, got %q", got)
	}
}
