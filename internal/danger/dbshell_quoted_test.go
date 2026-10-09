package danger

import "testing"

// Text inside an SQL string literal and ordinary SQL words are data, not
// client commands: only a meta-command position carries the escape.
func TestRED_DBClientShellEscapeIgnoresSQLText(t *testing.T) {
	for _, cmd := range []string{
		`psql mydb <<< 'select * from system where x=1'`,
		`psql -c "select '\! not a command' as x" mydb`,
		`psql -c "select * from system where x=1" mydb`,
		`mysql -e 'select * from system where x=1'`,
		`mysql -e 'select pager from t'`,
		`mysql -e "select * from t where c = 'a;system x'"`,
		`echo "select 'x \! y'" | psql mydb`,
	} {
		if got := Classify(cmd); got != NetworkEgress {
			t.Errorf("Classify(%q) = %s, want network_egress", cmd, got)
		}
	}
}

// The quote-aware scan must still see every real escape, including ones placed
// after comments whose apostrophes could pair with a later quote.
func TestRED_DBClientShellTextKeepsRealEscapes(t *testing.T) {
	for _, text := range []string{
		`\! id`,
		"select 1 \\! id",
		"select 'a'; \\! id",
		"select 1; system id",
		"select 1\nsystem id",
		"system id",
		"pager sh",
		"select 1 -- it's\n\\! id -- x's",
		"select 1 /* it's */ \\! id /* x's */",
		"select 1 # it's\n\\! id # x's",
		"select 'unterminated \\! id",
		"select $q$ '$q$ \\! id",
		`\copy t to program 'id'`,
	} {
		if !dbClientShellText(text) {
			t.Errorf("dbClientShellText(%q) = false, want true", text)
		}
	}
	for _, text := range []string{
		"select * from system where x=1",
		"select '\\! id'",
		"select \"\\! id\"",
		"select 'a;system x'",
		"select pager from t",
	} {
		if dbClientShellText(text) {
			t.Errorf("dbClientShellText(%q) = true, want false", text)
		}
	}
}
