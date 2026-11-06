package mysql

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func TestParseShowGrants(t *testing.T) {
	cases := []struct {
		name string
		line string
		want []grantEntry
	}{
		{
			name: "database level",
			line: "GRANT SELECT, INSERT ON `app`.* TO `u`@`h`",
			want: []grantEntry{{Privileges: []string{"SELECT", "INSERT"}, Database: "app", Table: "*"}},
		},
		{
			name: "all privileges with grant option on a table",
			line: "GRANT ALL PRIVILEGES ON `app`.`t` TO `u`@`h` WITH GRANT OPTION",
			want: []grantEntry{{Privileges: []string{"ALL PRIVILEGES"}, Database: "app", Table: "t", GrantOption: true}},
		},
		{
			name: "ALL is normalised",
			line: "GRANT ALL ON `app`.* TO `u`@`h`",
			want: []grantEntry{{Privileges: []string{"ALL PRIVILEGES"}, Database: "app", Table: "*"}},
		},
		{
			name: "column privileges",
			line: "GRANT SELECT (`c1`, `c2`), UPDATE ON `app`.`t` TO `u`@`h`",
			want: []grantEntry{{Privileges: []string{"SELECT (c1, c2)", "UPDATE"}, Database: "app", Table: "t"}},
		},
		{
			name: "column privileges without spaces and mixed case",
			line: "grant select(`a`,`B`) on `app`.`t` to `u`@`h`",
			want: []grantEntry{{Privileges: []string{"SELECT (a, B)"}, Database: "app", Table: "t"}},
		},
		{
			name: "comma and keyword inside backticks",
			line: "GRANT SELECT ON `we,ird ON name`.`t.1` TO `u`@`h`",
			want: []grantEntry{{Privileges: []string{"SELECT"}, Database: "we,ird ON name", Table: "t.1"}},
		},
		{
			name: "doubled backtick",
			line: "GRANT SELECT ON `a``b`.* TO `u`@`h`",
			want: []grantEntry{{Privileges: []string{"SELECT"}, Database: "a`b", Table: "*"}},
		},
		{
			name: "wildcard database",
			line: "GRANT SELECT ON `app\\_%`.* TO `u`@`h`",
			want: []grantEntry{{Privileges: []string{"SELECT"}, Database: `app\_%`, Table: "*"}},
		},
		{
			name: "global",
			line: "GRANT SELECT, RELOAD ON *.* TO `u`@`h`",
			want: []grantEntry{{Privileges: []string{"SELECT", "RELOAD"}, Database: "*", Table: "*"}},
		},
		{
			name: "global with extra whitespace",
			line: "  GRANT  select ,  create   view ON *.* TO `u`@`h`  ",
			want: []grantEntry{{Privileges: []string{"SELECT", "CREATE VIEW"}, Database: "*", Table: "*"}},
		},
		{name: "usage only", line: "GRANT USAGE ON *.* TO `u`@`h`", want: nil},
		{name: "usage with grant option", line: "GRANT USAGE ON *.* TO `u`@`h` WITH GRANT OPTION", want: nil},
		{name: "proxy", line: "GRANT PROXY ON ''@'' TO 'root'@'localhost' WITH GRANT OPTION", want: nil},
		{name: "procedure", line: "GRANT EXECUTE ON PROCEDURE `app`.`p` TO `u`@`h`", want: nil},
		{
			name: "table keyword",
			line: "GRANT SELECT ON TABLE `app`.`t` TO `u`@`h`",
			want: []grantEntry{{Privileges: []string{"SELECT"}, Database: "app", Table: "t"}},
		},
		{
			name: "roles",
			line: "GRANT `role1`@`%`,`role2`@`%` TO `u`@`h`",
			want: []grantEntry{{Roles: []string{"role1", "role2"}}},
		},
		{
			name: "role with host",
			line: "GRANT `r`@`10.0.0.1` TO `u`@`h` WITH ADMIN OPTION",
			want: []grantEntry{{Roles: []string{"r@10.0.0.1"}}},
		},
		{name: "empty", line: "", want: nil},
		{name: "whitespace", line: "   ", want: nil},
		{name: "garbage", line: "not a grant", want: nil},
		{name: "only GRANT", line: "GRANT", want: nil},
		{name: "GRANT with space only", line: "GRANT ", want: nil},
		{name: "missing TO", line: "GRANT SELECT ON `app`.*", want: nil},
		{name: "missing table", line: "GRANT SELECT ON `app` TO `u`@`h`", want: nil},
		{name: "unbalanced paren", line: "GRANT SELECT (`a` ON `app`.`t` TO `u`@`h`", want: nil},
		{name: "unterminated backtick", line: "GRANT SELECT ON `app.* TO `u`@`h`", want: nil},
		{name: "role grant without TO", line: "GRANT `r`@`%`", want: nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := parseShowGrants([]string{c.line})
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("parseShowGrants(%q) = %#v, want %#v", c.line, got, c.want)
			}
		})
	}
}

func TestParseShowGrants_multipleLines(t *testing.T) {
	got := parseShowGrants([]string{
		"GRANT USAGE ON *.* TO `u`@`h`",
		"GRANT SELECT ON `a`.* TO `u`@`h`",
		"garbage",
		"GRANT `r`@`%` TO `u`@`h`",
		"GRANT INSERT ON `b`.`t` TO `u`@`h` WITH GRANT OPTION",
	})
	want := []grantEntry{
		{Privileges: []string{"SELECT"}, Database: "a", Table: "*"},
		{Roles: []string{"r"}},
		{Privileges: []string{"INSERT"}, Database: "b", Table: "t", GrantOption: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if got := parseShowGrants(nil); got != nil {
		t.Errorf("parseShowGrants(nil) = %#v, want nil", got)
	}
}

func TestFindGrantEntry(t *testing.T) {
	entries := []grantEntry{
		{Roles: []string{"r"}},
		{Privileges: []string{"SELECT"}, Database: "app", Table: "t"},
		{Privileges: []string{"INSERT"}, Database: "app", Table: "*"},
		{Privileges: []string{"DELETE"}, Database: "*", Table: "*"},
	}
	cases := []struct {
		db, table string
		want      string
		ok        bool
	}{
		{"app", "", "INSERT", true},
		{"app", "*", "INSERT", true},
		{"`app`", "`t`", "SELECT", true},
		{"*", "*", "DELETE", true},
		{"other", "*", "", false},
		{"app", "missing", "", false},
	}
	for _, c := range cases {
		e, ok := findGrantEntry(entries, c.db, c.table)
		if ok != c.ok || (ok && e.Privileges[0] != c.want) {
			t.Errorf("findGrantEntry(%q, %q) = %v, %v; want %s, %v", c.db, c.table, e, ok, c.want, c.ok)
		}
	}
}

func TestFindGrantEntry_mergesDynamicLine(t *testing.T) {
	entries := parseShowGrants([]string{
		"GRANT SELECT, INSERT ON *.* TO `u`@`h`",
		"GRANT APPLICATION_PASSWORD_ADMIN, XA_RECOVER_ADMIN ON *.* TO `u`@`h` WITH GRANT OPTION",
	})
	e, ok := findGrantEntry(entries, "*", "*")
	want := []string{"SELECT", "INSERT", "APPLICATION_PASSWORD_ADMIN", "XA_RECOVER_ADMIN"}
	if !ok || !reflect.DeepEqual(e.Privileges, want) || !e.GrantOption {
		t.Errorf("got %#v, %v", e, ok)
	}
}

func TestNormalizePrivilege(t *testing.T) {
	cases := map[string]string{
		"select":               "SELECT",
		" ALL ":                "ALL PRIVILEGES",
		"all privileges":       "ALL PRIVILEGES",
		"create   view":        "CREATE VIEW",
		"select (a,b)":         "SELECT (a, b)",
		"UPDATE (`c1`, `c2` )": "UPDATE (c1, c2)",
		"":                     "",
		"REFERENCES (`x`)":     "REFERENCES (x)",
	}
	for in, want := range cases {
		if got := normalizePrivilege(in); got != want {
			t.Errorf("normalizePrivilege(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestGrantPrivilegesDiffSuppress checks that ALL and ALL PRIVILEGES do not
// produce a plan, while a real change does.
func TestGrantPrivilegesDiffSuppress(t *testing.T) {
	r := resourceGrant()
	state := func(privileges ...string) *terraform.InstanceState {
		attrs := map[string]string{
			"id":         "u@h:`app`",
			"user":       "u",
			"host":       "h",
			"database":   "app",
			"table":      "*",
			"grant":      "false",
			"tls_option": "NONE",
		}
		attrs["privileges.#"] = strconv.Itoa(len(privileges))
		for _, p := range privileges {
			attrs["privileges."+strconv.Itoa(hashPrivilege(p))] = p
		}
		return &terraform.InstanceState{ID: "u@h:`app`", Attributes: attrs}
	}
	config := func(privileges ...interface{}) *terraform.ResourceConfig {
		return terraform.NewResourceConfigRaw(map[string]interface{}{
			"user":       "u",
			"host":       "h",
			"database":   "app",
			"privileges": privileges,
		})
	}

	cases := []struct {
		name     string
		state    *terraform.InstanceState
		config   *terraform.ResourceConfig
		wantDiff bool
	}{
		{"ALL vs ALL PRIVILEGES", state("ALL PRIVILEGES"), config("ALL"), false},
		{"lower case", state("SELECT", "INSERT"), config("select", "insert"), false},
		{"column spacing", state("SELECT (a, b)"), config("SELECT (a,b)"), false},
		{"changed privilege", state("SELECT", "INSERT"), config("SELECT"), true},
		{"ALL vs SELECT", state("ALL PRIVILEGES"), config("SELECT"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			diff, err := r.Diff(context.Background(), c.state, c.config, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := diff != nil && !diff.Empty(); got != c.wantDiff {
				t.Errorf("diff present = %v, want %v (diff: %v)", got, c.wantDiff, diff)
			}
		})
	}
}
