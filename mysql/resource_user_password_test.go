package mysql

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/helper/resource"
	"github.com/keybase/go-crypto/openpgp"
)

// testAccPGPPublicKey generates a throwaway PGP key and returns its base64
// encoded public part, so the test does not depend on keybase.io being up.
func testAccPGPPublicKey(t *testing.T) string {
	t.Helper()
	entity, err := openpgp.NewEntity("tf-acc-test", "", "tf-acc-test@example.com", nil)
	if err != nil {
		t.Fatalf("generating PGP key: %s", err)
	}
	// SerializePrivate signs the identities, which Serialize requires.
	if err := entity.SerializePrivate(io.Discard, nil); err != nil {
		t.Fatalf("signing PGP key: %s", err)
	}
	var buf bytes.Buffer
	if err := entity.Serialize(&buf); err != nil {
		t.Fatalf("serializing PGP key: %s", err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestAccUserPassword_basic(t *testing.T) {
	pgpKey := testAccPGPPublicKey(t)
	resource.Test(t, resource.TestCase{
		PreCheck:     func() { testAccPreCheck(t) },
		Providers:    testAccProviders,
		CheckDestroy: testAccUserCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccUserPasswordConfig_basic(pgpKey),
				Check: resource.ComposeTestCheckFunc(
					testAccUserExists("mysql_user.test"),
					resource.TestCheckResourceAttr("mysql_user_password.test", "user", "jdoe"),
					resource.TestCheckResourceAttrSet("mysql_user_password.test", "encrypted_password"),
				),
			},
		},
	})
}

func testAccUserPasswordConfig_basic(pgpKey string) string {
	return fmt.Sprintf(`
resource "mysql_user" "test" {
  user = "jdoe"
}

resource "mysql_user_password" "test" {
  user    = "${mysql_user.test.user}"
  pgp_key = "%s"
}
`, pgpKey)
}
