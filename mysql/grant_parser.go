package mysql

import (
	"strings"
)

// grantEntry is one parsed line of SHOW GRANTS output.
//
// Privilege grants carry Privileges, Database and Table; role grants carry
// Roles only (MySQL does not attach a database to them).
type grantEntry struct {
	Privileges  []string
	Database    string
	Table       string
	Roles       []string
	GrantOption bool
}

// parseShowGrants parses the rows returned by SHOW GRANTS FOR ....
//
// Normalisation rules:
//   - database and table names have their backticks removed ("*" is kept as is);
//   - privilege keywords are upper-cased, internal whitespace is collapsed and
//     "ALL" becomes "ALL PRIVILEGES";
//   - column privileges are rendered as "SELECT (c1, c2)" (backticks removed,
//     column name case preserved);
//   - roles are rendered as "name" when the host is "%" and "name@host" otherwise.
//
// USAGE-only entries, PROXY grants, routine grants (PROCEDURE/FUNCTION) and
// lines that cannot be parsed are skipped.
func parseShowGrants(lines []string) []grantEntry {
	var entries []grantEntry
	for _, line := range lines {
		if e, ok := parseGrantLine(line); ok {
			entries = append(entries, e)
		}
	}
	return entries
}

func parseGrantLine(line string) (grantEntry, bool) {
	line = strings.TrimSpace(line)
	const prefix = "GRANT "
	if len(line) <= len(prefix) || !strings.EqualFold(line[:len(prefix)], prefix) {
		return grantEntry{}, false
	}
	rest := strings.TrimSpace(line[len(prefix):])

	onIdx := indexTopLevel(rest, " ON ")
	if onIdx < 0 {
		return parseRoleGrant(rest)
	}

	privPart := strings.TrimSpace(rest[:onIdx])
	after := rest[onIdx+len(" ON "):]
	toIdx := indexTopLevel(after, " TO ")
	if toIdx < 0 {
		return grantEntry{}, false
	}
	target := strings.TrimSpace(after[:toIdx])
	tail := after[toIdx+len(" TO "):]

	if strings.EqualFold(privPart, "PROXY") {
		return grantEntry{}, false
	}
	upperTarget := strings.ToUpper(target)
	switch {
	case strings.HasPrefix(upperTarget, "PROCEDURE "), strings.HasPrefix(upperTarget, "FUNCTION "):
		return grantEntry{}, false
	case strings.HasPrefix(upperTarget, "TABLE "):
		target = strings.TrimSpace(target[len("TABLE "):])
	}

	parts := splitTopLevel(target, '.')
	if len(parts) != 2 {
		return grantEntry{}, false
	}

	var privileges []string
	for _, p := range splitTopLevel(privPart, ',') {
		p = normalizePrivilege(p)
		if p == "" || p == "USAGE" {
			continue
		}
		privileges = append(privileges, p)
	}
	if len(privileges) == 0 {
		return grantEntry{}, false
	}

	return grantEntry{
		Privileges:  privileges,
		Database:    unwrapBackticks(strings.TrimSpace(parts[0])),
		Table:       unwrapBackticks(strings.TrimSpace(parts[1])),
		GrantOption: indexTopLevel(tail, " WITH GRANT OPTION") >= 0,
	}, true
}

// parseRoleGrant parses "`r1`@`%`,`r2`@`%` TO `u`@`h`" (the text after GRANT).
func parseRoleGrant(rest string) (grantEntry, bool) {
	toIdx := indexTopLevel(rest, " TO ")
	if toIdx < 0 {
		return grantEntry{}, false
	}
	var roles []string
	for _, r := range splitTopLevel(rest[:toIdx], ',') {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		nameHost := splitTopLevel(r, '@')
		name := unwrapBackticks(strings.TrimSpace(nameHost[0]))
		if name == "" {
			continue
		}
		if len(nameHost) == 2 {
			host := unwrapBackticks(strings.TrimSpace(nameHost[1]))
			if host != "" && host != "%" {
				name += "@" + host
			}
		}
		roles = append(roles, name)
	}
	if len(roles) == 0 {
		return grantEntry{}, false
	}
	return grantEntry{Roles: roles}, true
}

// normalizePrivilege returns the canonical form of one privilege, e.g.
// "select" -> "SELECT", "ALL" -> "ALL PRIVILEGES",
// "SELECT (`a`,`b`)" -> "SELECT (a, b)".
func normalizePrivilege(p string) string {
	p = strings.TrimSpace(p)
	keyword, cols := p, ""
	if i := strings.Index(p, "("); i >= 0 {
		keyword = p[:i]
		inner := strings.TrimSpace(p[i+1:])
		inner = strings.TrimSpace(strings.TrimSuffix(inner, ")"))
		var names []string
		for _, c := range splitTopLevel(inner, ',') {
			if c = unwrapBackticks(strings.TrimSpace(c)); c != "" {
				names = append(names, c)
			}
		}
		cols = " (" + strings.Join(names, ", ") + ")"
	}
	keyword = strings.ToUpper(strings.Join(strings.Fields(keyword), " "))
	if keyword == "ALL" {
		keyword = "ALL PRIVILEGES"
	}
	if keyword == "" {
		return ""
	}
	return keyword + cols
}

// walkTopLevel calls fn for each byte offset of s that is outside backticks,
// quotes and parentheses. fn returns true to stop the walk.
func walkTopLevel(s string, fn func(i int) bool) {
	var quote byte
	depth := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if quote != '`' && c == '\\' {
				i++
			} else if c == quote {
				quote = 0
			}
			continue
		}
		switch c {
		case '`', '\'', '"':
			quote = c
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 && fn(i) {
				return
			}
		}
	}
}

// indexTopLevel is a case-insensitive strings.Index that ignores matches inside
// backticks, quotes and parentheses.
func indexTopLevel(s, sub string) int {
	found := -1
	walkTopLevel(s, func(i int) bool {
		if len(s)-i >= len(sub) && strings.EqualFold(s[i:i+len(sub)], sub) {
			found = i
			return true
		}
		return false
	})
	return found
}

// splitTopLevel splits s on sep, ignoring separators inside backticks, quotes
// and parentheses.
func splitTopLevel(s string, sep byte) []string {
	var parts []string
	start := 0
	walkTopLevel(s, func(i int) bool {
		if s[i] == sep {
			parts = append(parts, s[start:i])
			start = i + 1
		}
		return false
	})
	return append(parts, s[start:])
}
