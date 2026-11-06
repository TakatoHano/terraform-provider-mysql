package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func TestAccUser_basic(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccUserCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccUserConfig_basic,
				Check: resource.ComposeTestCheckFunc(
					testAccUserExists("mysql_user.test"),
					resource.TestCheckResourceAttr("mysql_user.test", "user", "jdoe"),
					resource.TestCheckResourceAttr("mysql_user.test", "host", "example.com"),
					resource.TestCheckResourceAttr("mysql_user.test", "plaintext_password", hashSum("password")),
					resource.TestCheckResourceAttr("mysql_user.test", "tls_option", "NONE"),
				),
			},
			{
				Config: testAccUserConfig_ssl,
				Check: resource.ComposeTestCheckFunc(
					testAccUserExists("mysql_user.test"),
					resource.TestCheckResourceAttr("mysql_user.test", "user", "jdoe"),
					resource.TestCheckResourceAttr("mysql_user.test", "host", "example.com"),
					resource.TestCheckResourceAttr("mysql_user.test", "plaintext_password", hashSum("password")),
					resource.TestCheckResourceAttr("mysql_user.test", "tls_option", "SSL"),
				),
			},
			{
				Config: testAccUserConfig_newPass,
				Check: resource.ComposeTestCheckFunc(
					testAccUserExists("mysql_user.test"),
					resource.TestCheckResourceAttr("mysql_user.test", "user", "jdoe"),
					resource.TestCheckResourceAttr("mysql_user.test", "host", "example.com"),
					resource.TestCheckResourceAttr("mysql_user.test", "plaintext_password", hashSum("password2")),
					resource.TestCheckResourceAttr("mysql_user.test", "tls_option", "NONE"),
				),
			},
		},
	})
}

func TestAccUser_importTLS(t *testing.T) {
	configs := []struct{ tls, alt string }{
		{"SSL", "ssl"},
		{"X509", "x509"},
		{"ISSUER '/CN=ca' AND SUBJECT '/CN=jdoe'", "subject '/CN=jdoe' and issuer '/CN=ca'"},
	}
	var steps []resource.TestStep
	for _, c := range configs {
		config := fmt.Sprintf(`
resource "mysql_user" "test" {
  user               = "jdoe-tls"
  host               = "example.com"
  plaintext_password = "password"
  tls_option         = %q
}
`, c.tls)
		steps = append(steps,
			resource.TestStep{
				Config: config,
				Check:  resource.TestCheckResourceAttr("mysql_user.test", "tls_option", c.tls),
			},
			resource.TestStep{
				ResourceName:            "mysql_user.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"plaintext_password", "password"},
			},
			// Reordered or differently cased clauses must not plan a replacement.
			resource.TestStep{
				Config:   strings.Replace(config, c.tls, c.alt, 1),
				PlanOnly: true,
			},
		)
	}
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccUserCheckDestroy,
		Steps:             steps,
	})
}

func TestAccUser_specialChars(t *testing.T) {
	user := "tf-acc-quote-user"
	password := `p'a\ss"w;d` + "`"
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccUserCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccUserConfig_specialChars(user, password),
				Check: resource.ComposeTestCheckFunc(
					testAccUserExists("mysql_user.test"),
					resource.TestCheckResourceAttr("mysql_user.test", "user", user),
					testAccCanConnectAs(user, password, "information_schema"),
				),
			},
			{
				Config: testAccUserConfig_specialChars(user, password+"'2"),
				Check: resource.ComposeTestCheckFunc(
					testAccCanConnectAs(user, password+"'2", "information_schema"),
				),
			},
		},
	})
}

func testAccUserConfig_specialChars(user, password string) string {
	return fmt.Sprintf(`
resource "mysql_user" "test" {
    user               = %q
    host               = "%%"
    plaintext_password = %q
}
`, user, password)
}

func TestAccUser_auth(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			// The mysql_no_login plugin ships with the server but is not loaded by default.
			db, err := connectToMySQL(context.Background(), testAccProvider.Meta().(*MySQLConfiguration))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec("INSTALL PLUGIN mysql_no_login SONAME 'mysql_no_login.so'"); err != nil {
				// 1125 = ER_UDF_EXISTS: the plugin is already installed.
				if me, ok := err.(*mysql.MySQLError); !ok || me.Number != 1125 {
					t.Skipf("mysql_no_login plugin cannot be installed: %s", err)
				}
			}
		},
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccUserCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccUserConfig_auth_iam_plugin,
				Check: resource.ComposeTestCheckFunc(
					testAccUserAuthExists("mysql_user.test"),
					resource.TestCheckResourceAttr("mysql_user.test", "user", "jdoe"),
					resource.TestCheckResourceAttr("mysql_user.test", "host", "example.com"),
					resource.TestCheckResourceAttr("mysql_user.test", "auth_plugin", "mysql_no_login"),
				),
			},
			{
				ResourceName:      "mysql_user.test",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

func TestAccUser_deprecated(t *testing.T) {
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccUserCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccUserConfig_deprecated,
				Check: resource.ComposeTestCheckFunc(
					testAccUserExists("mysql_user.test"),
					resource.TestCheckResourceAttr("mysql_user.test", "user", "jdoe"),
					resource.TestCheckResourceAttr("mysql_user.test", "host", "example.com"),
					resource.TestCheckResourceAttr("mysql_user.test", "password", "password"),
				),
			},
			{
				Config: testAccUserConfig_deprecated_newPass,
				Check: resource.ComposeTestCheckFunc(
					testAccUserExists("mysql_user.test"),
					resource.TestCheckResourceAttr("mysql_user.test", "user", "jdoe"),
					resource.TestCheckResourceAttr("mysql_user.test", "host", "example.com"),
					resource.TestCheckResourceAttr("mysql_user.test", "password", "password2"),
				),
			},
		},
	})
}

func testAccUserExists(rn string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[rn]
		if !ok {
			return fmt.Errorf("resource not found: %s", rn)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("user id not set")
		}

		db, err := connectToMySQL(context.Background(), testAccProvider.Meta().(*MySQLConfiguration))
		if err != nil {
			return err
		}

		stmtSQL := fmt.Sprintf("SELECT count(*) from mysql.user where CONCAT(user, '@', host) = '%s'", rs.Primary.ID)
		log.Println("Executing statement:", stmtSQL)
		var count int
		err = db.QueryRow(stmtSQL).Scan(&count)
		if err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("expected 1 row reading user but got no rows")
			}
			return fmt.Errorf("error reading user: %s", err)
		}

		return nil
	}
}

func testAccUserAuthExists(rn string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[rn]
		if !ok {
			return fmt.Errorf("resource not found: %s", rn)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("user id not set")
		}

		db, err := connectToMySQL(context.Background(), testAccProvider.Meta().(*MySQLConfiguration))
		if err != nil {
			return err
		}

		stmtSQL := fmt.Sprintf("SELECT count(*) from mysql.user where CONCAT(user, '@', host) = '%s' and plugin = 'mysql_no_login'", rs.Primary.ID)
		log.Println("Executing statement:", stmtSQL)
		var count int
		err = db.QueryRow(stmtSQL).Scan(&count)
		if err != nil {
			if err == sql.ErrNoRows {
				return fmt.Errorf("expected 1 row reading user but got no rows")
			}
			return fmt.Errorf("error reading user: %s", err)
		}

		return nil
	}
}

func testAccUserCheckDestroy(s *terraform.State) error {
	db, err := connectToMySQL(context.Background(), testAccProvider.Meta().(*MySQLConfiguration))
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "mysql_user" {
			continue
		}

		stmtSQL := fmt.Sprintf("SELECT user from mysql.user where CONCAT(user, '@', host) = '%s'", rs.Primary.ID)
		log.Println("Executing statement:", stmtSQL)
		rows, err := db.Query(stmtSQL)
		if err != nil {
			return fmt.Errorf("error issuing query: %s", err)
		}
		defer rows.Close()
		if rows.Next() {
			return fmt.Errorf("user still exists after destroy")
		}
	}
	return nil
}

const testAccUserConfig_basic = `
resource "mysql_user" "test" {
    user = "jdoe"
    host = "example.com"
    plaintext_password = "password"
}
`

const testAccUserConfig_ssl = `
resource "mysql_user" "test" {
    user = "jdoe"
    host = "example.com"
	plaintext_password = "password"
	tls_option = "SSL"
}
`

const testAccUserConfig_newPass = `
resource "mysql_user" "test" {
    user = "jdoe"
    host = "example.com"
    plaintext_password = "password2"
}
`

const testAccUserConfig_deprecated = `
resource "mysql_user" "test" {
    user = "jdoe"
    host = "example.com"
    password = "password"
}
`

const testAccUserConfig_deprecated_newPass = `
resource "mysql_user" "test" {
    user = "jdoe"
    host = "example.com"
    password = "password2"
}
`

const testAccUserConfig_auth_iam_plugin = `
resource "mysql_user" "test" {
    user        = "jdoe"
    host        = "example.com"
    auth_plugin = "mysql_no_login"
}
`
