package danger

import "testing"

// Inside double quotes the shell keeps the backslash of a client meta-command,
// so the escape must survive normalization.
func TestRED_DBClientShellEscapeInDoubleQuotes(t *testing.T) {
	for _, cmd := range []string{
		`psql -c "\! id" db`,
		`psql -c "select 1 \! id" db`,
		`psql -c "select 1 \g | sh" db`,
		`psql -c "select 1 \o | sh" db`,
	} {
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
		}
	}
	for _, cmd := range []string{
		`psql -c "select 'a' || 'b'" db`,
		`psql -c "select 1 as g" db`,
	} {
		if got := Classify(cmd); got != NetworkEgress {
			t.Errorf("Classify(%q) = %s, want network_egress", cmd, got)
		}
	}
}
