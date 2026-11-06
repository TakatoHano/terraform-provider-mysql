package mysql

import "testing"

func TestQuoteString(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain", `'plain'`},
		{"", `''`},
		{"it's", `'it''s'`},
		{`a\b`, `'a\\b'`},
		{`\'; DROP USER 'x`, `'\\''; DROP USER ''x'`},
		{"''", `''''''`},
	}
	for _, c := range cases {
		if got := quoteString(c.in); got != c.want {
			t.Errorf("quoteString(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestUserHostLiteral(t *testing.T) {
	cases := []struct{ user, host, want string }{
		{"jdoe", "localhost", `'jdoe'@'localhost'`},
		{"o'brien", "%", `'o''brien'@'%'`},
		{"a-b", `h\x`, `'a-b'@'h\\x'`},
	}
	for _, c := range cases {
		if got := userHostLiteral(c.user, c.host); got != c.want {
			t.Errorf("userHostLiteral(%q, %q) = %s, want %s", c.user, c.host, got, c.want)
		}
	}
}

func TestQuoteIdentifier(t *testing.T) {
	cases := []struct{ in, want string }{
		{"db", "`db`"},
		{"a-b", "`a-b`"},
		{"a`b", "`a``b`"},
		{"", "``"},
	}
	for _, c := range cases {
		if got := quoteIdentifier(c.in); got != c.want {
			t.Errorf("quoteIdentifier(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestFormatDatabaseName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"*", "*"},
		{"app", "`app`"},
		{"tf-acc-quote", "`tf-acc-quote`"},
		{"`app`", "`app`"},
		{"`a``b`", "`a``b`"},
		{"a`b", "`a``b`"},
		{`app\_%`, "`app\\_%`"},
		{"`app\\_%`", "`app\\_%`"},
	}
	for _, c := range cases {
		if got := formatDatabaseName(c.in); got != c.want {
			t.Errorf("formatDatabaseName(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestFormatTableName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "*"},
		{"*", "*"},
		{"t", "`t`"},
		{"my-table", "`my-table`"},
		{"`t`", "`t`"},
		{"a`b", "`a``b`"},
	}
	for _, c := range cases {
		if got := formatTableName(c.in); got != c.want {
			t.Errorf("formatTableName(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestValidateTLSOption(t *testing.T) {
	valid := []string{
		"NONE", "SSL", "X509", "none", "ssl",
		"CIPHER 'EDH-RSA-DES-CBC3-SHA'",
		"ISSUER '/C=SE/CN=CA'",
		"SUBJECT 'x'",
		"ISSUER 'a' AND SUBJECT 'b'",
		"issuer 'a' and cipher 'b' AND subject 'c'",
		"CIPHER ''",
	}
	invalid := []string{
		"", "SSL; DROP USER x", "SSL'", "CIPHER 'a'b'", "CIPHER 'a' OR SUBJECT 'b'",
		`CIPHER 'a\' AND ISSUER 'b'`, `SUBJECT 'a\b'`, "CIPHER a", "SSL AND X509", "SSL --", "NONE ", "CIPHER 'a' AND",
	}
	for _, v := range valid {
		if _, errs := validateTLSOption(v, "tls_option"); len(errs) != 0 {
			t.Errorf("validateTLSOption(%q) rejected: %v", v, errs)
		}
	}
	for _, v := range invalid {
		if _, errs := validateTLSOption(v, "tls_option"); len(errs) == 0 {
			t.Errorf("validateTLSOption(%q) accepted", v)
		}
	}
}

func TestValidatePrivilege(t *testing.T) {
	valid := []string{
		"SELECT", "select", "ALL PRIVILEGES", "ALL", "CREATE TEMPORARY TABLES",
		"SELECT (col1, col2)", "SELECT(col1)", "UPDATE (`a`)", "  SELECT  ",
		"INSERT (`my-col`, b)",
	}
	invalid := []string{
		"", "   ", "SELECT'", `SELECT"`, "SELECT;", `SELECT\`, "SELECT; DROP USER x",
		"SELECT (a", "SELECT a)", "SELECT ((a))", "SELECT (a'b)", "SELECT (a) (b)",
		"SELECT, UPDATE", "SELECT /* x */", "1SELECT", "SELECT (a;b)",
	}
	for _, v := range valid {
		if _, errs := validatePrivilege(v, "privileges"); len(errs) != 0 {
			t.Errorf("validatePrivilege(%q) rejected: %v", v, errs)
		}
	}
	for _, v := range invalid {
		if _, errs := validatePrivilege(v, "privileges"); len(errs) == 0 {
			t.Errorf("validatePrivilege(%q) accepted", v)
		}
	}
}
