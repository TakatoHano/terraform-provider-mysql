package mysql

import (
	"context"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// To run these acceptance tests, you will need access to a MySQL server.
// Amazon RDS is one way to get a MySQL server. If you use RDS, you can
// use the root account credentials you specified when creating an RDS
// instance to get the access necessary to run these tests. (the tests
// assume full access to the server.)
//
// Set the MYSQL_ENDPOINT and MYSQL_USERNAME environment variables before
// running the tests. If the given user has a password then you will also need
// to set MYSQL_PASSWORD.
//
// The tests assume a reasonably-vanilla MySQL configuration. In particular,
// they assume that the "utf8" character set is available and that
// "utf8_bin" is a valid collation that isn't the default for that character
// set.
//
// You can run the tests like this:
//    make testacc TEST=./builtin/providers/mysql

var testAccProviderFactories map[string]func() (*schema.Provider, error)
var testAccProvider *schema.Provider

func init() {
	testAccProvider = Provider()
	testAccProviderFactories = map[string]func() (*schema.Provider, error){
		"mysql": func() (*schema.Provider, error) { return testAccProvider, nil },
	}
}

func TestProvider(t *testing.T) {
	if err := Provider().InternalValidate(); err != nil {
		t.Fatalf("err: %s", err)
	}
}

func testAccPreCheck(t *testing.T) {
	for _, name := range []string{"MYSQL_ENDPOINT", "MYSQL_USERNAME"} {
		if v := os.Getenv(name); v == "" {
			t.Fatal("MYSQL_ENDPOINT, MYSQL_USERNAME and optionally MYSQL_PASSWORD must be set for acceptance tests")
		}
	}

	diags := testAccProvider.Configure(context.Background(), terraform.NewResourceConfigRaw(nil))
	if diags.HasError() {
		t.Fatal(diags)
	}
}
