package danger

import "testing"

func TestDBClientShellEscapeVariants(t *testing.T) {
	for _, cmd := range []string{
		`psql -c '\o | sh' db`,
		`psql -c 'select 1 \g | tee /tmp/x' db`,
		`mysql -e 'pager sh; select 1'`,
		`mysql --pager=sh -e 'select 1'`,
		`mariadb -e '\! id'`,
		`pgcli -c '\! id'`,
	} {
		if got := Classify(cmd); Rank(got) < Rank(CodeExecution) {
			t.Errorf("Classify(%q) = %s, want at least code_execution", cmd, got)
		}
	}
	// Plain queries keep the network class.
	for _, cmd := range []string{
		`psql -h db -c 'select * from systems' mydb`,
		`mysql -e 'select 1'`,
		`psql -c 'select 1 \g' db`,
		`mysql -e 'show databases'`,
	} {
		if got := Classify(cmd); got != NetworkEgress {
			t.Errorf("Classify(%q) = %s, want network_egress", cmd, got)
		}
	}
	if dbClientRunsShell("curl", []string{"curl", `\!`}) {
		t.Error("only database clients carry shell escapes")
	}
}
