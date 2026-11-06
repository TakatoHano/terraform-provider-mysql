package mysql

import "testing"

func TestUserTLSOptionMapping(t *testing.T) {
	cases := []struct {
		name string
		in   userTLSAuth
		want string
		ok   bool
	}{
		{"none", userTLSAuth{}, "NONE", true},
		{"ssl", userTLSAuth{sslType: "ANY"}, "SSL", true},
		{"x509", userTLSAuth{sslType: "X509"}, "X509", true},
		{"all parts", userTLSAuth{sslType: "SPECIFIED", sslCipher: "AES", x509Issuer: "/CN=ca", x509Subject: "/CN=me"},
			"CIPHER 'AES' AND ISSUER '/CN=ca' AND SUBJECT '/CN=me'", true},
		{"only subject", userTLSAuth{sslType: "SPECIFIED", x509Subject: "/CN=me"}, "SUBJECT '/CN=me'", true},
		{"cipher and issuer", userTLSAuth{sslType: "SPECIFIED", sslCipher: "AES", x509Issuer: "ca"}, "CIPHER 'AES' AND ISSUER 'ca'", true},
		{"quote in value", userTLSAuth{sslType: "SPECIFIED", x509Subject: "O'Neil"}, "", false},
		{"backslash in value", userTLSAuth{sslType: "SPECIFIED", x509Subject: `a\b`}, "", false},
		{"specified but empty", userTLSAuth{sslType: "SPECIFIED"}, "", false},
		{"unknown", userTLSAuth{sslType: "WHATEVER"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := c.in.tlsOption()
			if got != c.want || ok != c.ok {
				t.Errorf("tlsOption() = %q, %v; want %q, %v", got, ok, c.want, c.ok)
			}
			if ok {
				if err := validateTLSOptionValue(got); err != nil {
					t.Errorf("mapped value %q fails validation: %s", got, err)
				}
			}
		})
	}
}

func validateTLSOptionValue(v string) error {
	if !tlsOptionRegexp.MatchString(v) {
		return errNotTLS
	}
	return nil
}

type tlsErr string

func (e tlsErr) Error() string { return string(e) }

const errNotTLS = tlsErr("does not match tlsOptionRegexp")

func TestCanonicalTLSOption(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ssl", "SSL"},
		{" SSL ", "SSL"},
		{"x509", "X509"},
		{"none", "NONE"},
		{"cipher 'AES'", "CIPHER 'AES'"},
		{"SUBJECT '/CN=me' and ISSUER '/CN=ca'  AND cipher 'AES'", "CIPHER 'AES' AND ISSUER '/CN=ca' AND SUBJECT '/CN=me'"},
		{"subject   'John Smith'", "SUBJECT 'John Smith'"},
		{"CIPHER 'a' AND BOGUS 'b'", "CIPHER 'A' AND BOGUS 'B'"},
		{"garbage", "GARBAGE"},
		{"CIPHER 'unterminated", "CIPHER 'UNTERMINATED"},
	}
	for _, c := range cases {
		if got := canonicalTLSOption(c.in); got != c.want {
			t.Errorf("canonicalTLSOption(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if !suppressEquivalentTLSOption("", "ssl", "SSL", nil) {
		t.Error("ssl and SSL should be equivalent")
	}
	if !suppressEquivalentTLSOption("", "ISSUER 'a' AND CIPHER 'b'", "CIPHER 'b' AND ISSUER 'a'", nil) {
		t.Error("reordered clauses should be equivalent")
	}
	if suppressEquivalentTLSOption("", "SSL", "X509", nil) {
		t.Error("SSL and X509 must differ")
	}
	if suppressEquivalentTLSOption("", "CIPHER 'a'", "CIPHER 'A'", nil) {
		t.Error("quoted values are case sensitive")
	}
}
