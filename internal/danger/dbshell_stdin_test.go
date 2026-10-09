package danger

import "testing"

// A psql/mysql shell escape reaches the client through stdin as easily as
// through -c: a static echo/printf pipe, a here-string, or a \copy ... program
// form (which runs the program locally without any pipe character).
func TestRED_DBClientShellEscapeViaStdinAndCopyProgram(t *testing.T) {
	for _, cmd := range []string{
		`echo '\! id' | psql mydb`,
		`printf '\\! id\n' | psql mydb`,
		`echo 'system id' | mysql`,
		`echo 'select 1; system id' | mysql mydb`,
		`psql mydb <<< '\! id'`,
		`mysql <<< 'system id'`,
		`psql -c "\copy t to program 'sh -c id'" mydb`,
		`psql -c "\copy (select 1) to program 'tee /tmp/x'" mydb`,
		`echo "\copy t from program 'id'" | psql mydb`,
	} {
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
		}
	}
}

// psql -f FILE runs the script file's meta-commands (including \!), so the file
// is a program operand the unread-script gate must see.
func TestRED_PsqlFileIsProgramOperand(t *testing.T) {
	_, licensed, other, _ := redRewriteSetup(t)
	if !redGated("psql -f " + other + " mydb") {
		t.Errorf("an unread psql -f script must gate")
	}
	if !redGated("psql --file=" + other + " mydb") {
		t.Errorf("an unread psql --file script must gate")
	}
	if redGated("psql -f " + licensed + " mydb") {
		t.Errorf("a read psql -f script must not gate")
	}
}

// Plain stdin queries keep the network class.
func TestDBClientStdinPlainQueriesStayNetwork(t *testing.T) {
	for _, cmd := range []string{
		`echo 'select 1' | psql mydb`,
	} {
		if got := Classify(cmd); got != NetworkEgress {
			t.Errorf("Classify(%q) = %s, want network_egress", cmd, got)
		}
	}
}
