package mysql

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

var identQuoteReplacer = strings.NewReplacer("`", "``")

// stringQuoteReplacer escapes backslashes and single quotes for use inside
// a single-quoted MySQL string literal.
var stringQuoteReplacer = strings.NewReplacer(`\`, `\\`, `'`, `''`)

// quoteIdentifier wraps an identifier in backticks, doubling embedded backticks.
func quoteIdentifier(in string) string {
	return fmt.Sprintf("`%s`", identQuoteReplacer.Replace(in))
}

// quoteString returns s as a single-quoted MySQL string literal.
//
// Doubling the single quote is correct under both the default sql_mode and
// NO_BACKSLASH_ESCAPES, so a quote can never terminate the literal. Backslashes
// are doubled for the default mode; under NO_BACKSLASH_ESCAPES they therefore
// end up as literal double backslashes. That is an accepted trade-off: the value
// is altered but no injection is possible.
func quoteString(s string) string {
	return "'" + stringQuoteReplacer.Replace(s) + "'"
}

// userHostLiteral returns 'user'@'host' with both parts escaped.
func userHostLiteral(user, host string) string {
	return quoteString(user) + "@" + quoteString(host)
}

// unwrapBackticks removes one pair of surrounding backticks (and un-doubles
// embedded ones) if s is wrapped in them.
func unwrapBackticks(s string) string {
	if len(s) >= 2 && strings.HasPrefix(s, "`") && strings.HasSuffix(s, "`") {
		return strings.ReplaceAll(s[1:len(s)-1], "``", "`")
	}
	return s
}

// formatDatabaseName quotes a database name for use in GRANT/REVOKE. "*" is kept
// as is; wildcards such as app\_% are legal inside a backticked name.
func formatDatabaseName(database string) string {
	if database == "*" {
		return "*"
	}
	return quoteIdentifier(unwrapBackticks(database))
}

// formatTableName quotes a table name for use in GRANT/REVOKE.
func formatTableName(table string) string {
	if table == "" || table == "*" {
		return "*"
	}
	return quoteIdentifier(unwrapBackticks(table))
}

var (
	tlsOptionRegexp = regexp.MustCompile(`(?i)^(NONE|SSL|X509|(CIPHER|ISSUER|SUBJECT) '[^']*'( AND (CIPHER|ISSUER|SUBJECT) '[^']*')*)$`)
	privilegeRegexp = regexp.MustCompile(`(?i)^[a-z_ ]+(\s*\([^()'";\\]*\))?$`)
)

var validateTLSOption = validation.StringMatch(
	tlsOptionRegexp,
	"must be NONE, SSL, X509 or a combination of CIPHER/ISSUER/SUBJECT '<value>' joined by AND",
)

// validatePrivilege checks one element of mysql_grant.privileges.
func validatePrivilege(i interface{}, k string) ([]string, []error) {
	v, ok := i.(string)
	if !ok {
		return nil, []error{fmt.Errorf("expected type of %q to be string", k)}
	}
	if !privilegeRegexp.MatchString(strings.TrimSpace(v)) {
		return nil, []error{fmt.Errorf("%q: %q is not a valid privilege (expected e.g. SELECT or SELECT (col1, col2))", k, v)}
	}
	return nil, nil
}
