package mysql

import (
	"context"
	"database/sql"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// userTLSAuth holds the columns of mysql.user that describe REQUIRE options
// and the authentication plugin.
type userTLSAuth struct {
	sslType, sslCipher, x509Issuer, x509Subject, plugin string
}

// queryUserTLSAuth reads the TLS and plugin columns of one account. It returns
// sql.ErrNoRows when the account does not exist.
func queryUserTLSAuth(ctx context.Context, db *sql.DB, user, host string) (userTLSAuth, error) {
	var sslType, sslCipher, issuer, subject, plugin sql.NullString
	err := db.QueryRowContext(ctx,
		"SELECT ssl_type, ssl_cipher, x509_issuer, x509_subject, plugin FROM mysql.user WHERE user = ? AND host = ?",
		user, host).Scan(&sslType, &sslCipher, &issuer, &subject, &plugin)
	if err != nil {
		return userTLSAuth{}, err
	}
	return userTLSAuth{
		sslType:     sslType.String,
		sslCipher:   sslCipher.String,
		x509Issuer:  issuer.String,
		x509Subject: subject.String,
		plugin:      plugin.String,
	}, nil
}

// tlsOption maps the mysql.user columns to a tls_option value. ok is false
// when the options cannot be represented (unknown ssl_type, or a value that
// validateTLSOption would reject because it contains a quote or backslash).
func (u userTLSAuth) tlsOption() (string, bool) {
	switch strings.ToUpper(u.sslType) {
	case "":
		return "NONE", true
	case "ANY":
		return "SSL", true
	case "X509":
		return "X509", true
	case "SPECIFIED":
		var parts []string
		for _, p := range []struct{ keyword, value string }{
			{"CIPHER", u.sslCipher},
			{"ISSUER", u.x509Issuer},
			{"SUBJECT", u.x509Subject},
		} {
			if p.value == "" {
				continue
			}
			if strings.ContainsAny(p.value, `'\`) {
				return "", false
			}
			parts = append(parts, p.keyword+" '"+p.value+"'")
		}
		if len(parts) == 0 {
			return "", false
		}
		return strings.Join(parts, " AND "), true
	}
	return "", false
}

var tlsClauseOrder = []string{"CIPHER", "ISSUER", "SUBJECT"}

// canonicalTLSOption normalises a tls_option value: keywords are upper-cased,
// whitespace is collapsed and CIPHER/ISSUER/SUBJECT clauses are put in MySQL's
// order. Quoted values are kept verbatim. Input that does not parse is only
// trimmed and upper-cased.
func canonicalTLSOption(s string) string {
	trimmed := strings.TrimSpace(s)
	switch up := strings.ToUpper(trimmed); up {
	case "NONE", "SSL", "X509":
		return up
	}

	clauses := map[string]string{}
	rest := trimmed
	for {
		rest = strings.TrimSpace(rest)
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return strings.ToUpper(trimmed)
		}
		keyword := strings.ToUpper(fields[0])
		rest = strings.TrimSpace(rest[len(fields[0]):])
		if !contains(tlsClauseOrder, keyword) || !strings.HasPrefix(rest, "'") {
			return strings.ToUpper(trimmed)
		}
		end := strings.Index(rest[1:], "'")
		if end < 0 {
			return strings.ToUpper(trimmed)
		}
		clauses[keyword] = rest[1 : 1+end]
		rest = strings.TrimSpace(rest[end+2:])
		if rest == "" {
			break
		}
		if len(rest) < 3 || !strings.EqualFold(rest[:3], "AND") {
			return strings.ToUpper(trimmed)
		}
		rest = rest[3:]
	}

	var parts []string
	for _, k := range tlsClauseOrder {
		if v, ok := clauses[k]; ok {
			parts = append(parts, k+" '"+v+"'")
		}
	}
	return strings.Join(parts, " AND ")
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func suppressEquivalentTLSOption(_, oldV, newV string, _ *schema.ResourceData) bool {
	return oldV != "" && newV != "" && canonicalTLSOption(oldV) == canonicalTLSOption(newV)
}
