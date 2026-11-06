package mysql

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

// TestAccPortForward_bastion exercises port_forward_client_config against a
// real sshd and MySQL. It needs:
//
//	MYSQL_PF_SSH_HOST     bastion address, e.g. 127.0.0.1:2222
//	MYSQL_PF_SSH_USER     SSH user
//	MYSQL_PF_SSH_KEY_PATH private key accepted by the bastion
//	MYSQL_PF_DB_ENDPOINT  MySQL address as seen from the bastion, e.g. mysql:3306
//	MYSQL_USERNAME / MYSQL_PASSWORD  MySQL credentials
//
// The CI "tunnel" job starts all of these; see .github/workflows/test.yml.
func TestAccPortForward_bastion(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance tests skipped unless TF_ACC is set")
	}
	env := map[string]string{}
	for _, k := range []string{"MYSQL_PF_SSH_HOST", "MYSQL_PF_SSH_USER", "MYSQL_PF_SSH_KEY_PATH", "MYSQL_PF_DB_ENDPOINT", "MYSQL_USERNAME"} {
		v := os.Getenv(k)
		if v == "" {
			t.Skipf("%s is not set", k)
		}
		env[k] = v
	}
	t.Cleanup(func() {
		if err := Cleanup(); err != nil {
			t.Errorf("Cleanup: %v", err)
		}
	})

	// A dedicated provider: the shared testAccProvider is configured from
	// MYSQL_ENDPOINT and must not be used here.
	p := Provider()
	factories := map[string]func() (*schema.Provider, error){
		"mysql": func() (*schema.Provider, error) { return p, nil },
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	localPort := l.Addr().(*net.TCPAddr).Port
	l.Close()

	const dbName = "terraform_acc_port_forward"
	cfg := fmt.Sprintf(`
provider "mysql" {
  endpoint = "127.0.0.1:%d"
  username = %q
  password = %q

  port_forward_client_config {
    remote_host  = %q
    db_endpoint  = %q
    ssh_user     = %q
    ssh_key_path = %q
  }
}

resource "mysql_database" "pf" {
  name = %q
}
`, localPort, env["MYSQL_USERNAME"], os.Getenv("MYSQL_PASSWORD"),
		env["MYSQL_PF_SSH_HOST"], env["MYSQL_PF_DB_ENDPOINT"],
		env["MYSQL_PF_SSH_USER"], env["MYSQL_PF_SSH_KEY_PATH"], dbName)

	exists := func(*terraform.State) error {
		meta, ok := p.Meta().(*MySQLConfiguration)
		if !ok || meta == nil {
			return fmt.Errorf("provider is not configured")
		}
		if meta.Config.Addr != fmt.Sprintf("127.0.0.1:%d", localPort) {
			return fmt.Errorf("provider connects to %q, not through the tunnel", meta.Config.Addr)
		}
		db, err := connectToMySQL(context.Background(), meta)
		if err != nil {
			return err
		}
		var name string
		return db.QueryRow("SELECT SCHEMA_NAME FROM information_schema.SCHEMATA WHERE SCHEMA_NAME = ?", dbName).Scan(&name)
	}

	resource.Test(t, resource.TestCase{
		ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check:  resource.ComposeTestCheckFunc(exists, resource.TestCheckResourceAttr("mysql_database.pf", "name", dbName)),
			},
			// A second step reconfigures the provider in the same process and
			// must reuse the existing tunnel instead of failing to bind.
			{
				Config: cfg,
				Check:  exists,
			},
		},
	})
}
