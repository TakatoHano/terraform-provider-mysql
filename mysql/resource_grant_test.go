package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func TestAccGrant(t *testing.T) {
	dbName := fmt.Sprintf("tf-test-%d", rand.Intn(100))
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccGrantCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccGrantConfig_basic(dbName),
				Check: resource.ComposeTestCheckFunc(
					testAccPrivilegeExists("mysql_grant.test", "SELECT"),
					resource.TestCheckResourceAttr("mysql_grant.test", "user", fmt.Sprintf("jdoe-%s", dbName)),
					resource.TestCheckResourceAttr("mysql_grant.test", "host", "example.com"),
					resource.TestCheckResourceAttr("mysql_grant.test", "database", dbName),
					resource.TestCheckResourceAttr("mysql_grant.test", "table", "*"),
					resource.TestCheckResourceAttr("mysql_grant.test", "tls_option", "NONE"),
				),
			},
			{
				Config: testAccGrantConfig_ssl(dbName),
				Check: resource.ComposeTestCheckFunc(
					testAccPrivilegeExists("mysql_grant.test", "SELECT"),
					resource.TestCheckResourceAttr("mysql_grant.test", "user", fmt.Sprintf("jdoe-%s", dbName)),
					resource.TestCheckResourceAttr("mysql_grant.test", "host", "example.com"),
					resource.TestCheckResourceAttr("mysql_grant.test", "database", dbName),
					resource.TestCheckResourceAttr("mysql_grant.test", "tls_option", "SSL"),
				),
			},
			{
				Config: testAccGrantConfig_withGrantOption(dbName),
				Check: resource.ComposeTestCheckFunc(
					testAccPrivilegeExists("mysql_grant.test", "WITH GRANT OPTION"),
					resource.TestCheckResourceAttr("mysql_grant.test", "grant", "true"),
				),
			},
		},
	})
}

func TestAccGrant_drift(t *testing.T) {
	dbName := fmt.Sprintf("tf-drift-%d", rand.Intn(100))
	user := fmt.Sprintf("jdoe-%s", dbName)
	config := testAccGrantConfig_drift(dbName)
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccGrantCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					testAccPrivilegeExists("mysql_grant.test", "INSERT"),
					resource.TestCheckResourceAttr("mysql_grant.test", "privileges.#", "2"),
				),
			},
			{
				ResourceName:            "mysql_grant.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"tls_option"},
			},
			{
				// Revoke a privilege behind Terraform's back: the plan must notice.
				PreConfig: func() {
					db, err := connectToMySQL(context.Background(), testAccProvider.Meta().(*MySQLConfiguration))
					if err != nil {
						t.Fatal(err)
					}
					stmt := fmt.Sprintf("REVOKE INSERT ON %s.* FROM %s", quoteIdentifier(dbName), userHostLiteral(user, "example.com"))
					if _, err := db.Exec(stmt); err != nil {
						t.Fatalf("%s: %s", stmt, err)
					}
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Applying replaces the grant and restores the privilege.
				Config: config,
				Check: resource.ComposeTestCheckFunc(
					testAccPrivilegeExists("mysql_grant.test", "INSERT"),
					resource.TestCheckResourceAttr("mysql_grant.test", "privileges.#", "2"),
				),
			},
		},
	})
}

func TestAccGrant_allGlobal(t *testing.T) {
	user := "jdoe-all-global"
	config := fmt.Sprintf(`
resource "mysql_user" "test" {
  user = %q
  host = "example.com"
}

resource "mysql_grant" "test" {
  user       = mysql_user.test.user
  host       = mysql_user.test.host
  database   = "*"
  table      = "*"
  privileges = ["ALL"]
}
`, user)
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccGrantCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  testAccPrivilegeExists("mysql_grant.test", "INSERT"),
			},
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				PreConfig: func() {
					db, err := connectToMySQL(context.Background(), testAccProvider.Meta().(*MySQLConfiguration))
					if err != nil {
						t.Fatal(err)
					}
					stmt := "REVOKE INSERT ON *.* FROM " + userHostLiteral(user, "example.com")
					if _, err := db.Exec(stmt); err != nil {
						t.Fatalf("%s: %s", stmt, err)
					}
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{
				// Replace the drifted grant so that destroy works cleanly.
				Config: config,
				Check:  testAccPrivilegeExists("mysql_grant.test", "INSERT"),
			},
		},
	})
}

func TestAccGrant_allPrivileges(t *testing.T) {
	dbName := fmt.Sprintf("tf-all-%d", rand.Intn(100))
	config := fmt.Sprintf(`
resource "mysql_database" "test" {
  name = "%s"
}

resource "mysql_user" "test" {
  user = "jdoe-%s"
  host = "example.com"
}

resource "mysql_grant" "test" {
  user       = mysql_user.test.user
  host       = mysql_user.test.host
  database   = mysql_database.test.name
  privileges = ["ALL"]
}
`, dbName, dbName)
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccGrantCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  testAccPrivilegeExists("mysql_grant.test", "ALL PRIVILEGES"),
			},
			{
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

func testAccGrantConfig_drift(dbName string) string {
	return fmt.Sprintf(`
resource "mysql_database" "test" {
  name = "%s"
}

resource "mysql_user" "test" {
  user = "jdoe-%s"
  host = "example.com"
}

resource "mysql_grant" "test" {
  user       = mysql_user.test.user
  host       = mysql_user.test.host
  database   = mysql_database.test.name
  privileges = ["SELECT", "INSERT"]
}
`, dbName, dbName)
}

func TestAccGrant_specialChars(t *testing.T) {
	dbName := "tf-acc-quote"
	user := "tf-acc-quote-user"
	password := `p'a\ss"w;d` + "`"
	resource.Test(t, resource.TestCase{
		PreCheck:          func() { testAccPreCheck(t) },
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccGrantCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccGrantConfig_specialChars(dbName, user, password),
				Check: resource.ComposeTestCheckFunc(
					testAccPrivilegeExists("mysql_grant.test", "SELECT"),
					resource.TestCheckResourceAttr("mysql_grant.test", "database", dbName),
					testAccCanConnectAs(user, password, dbName),
				),
			},
		},
	})
}

func testAccGrantConfig_withGrantOption(dbName string) string {
	return fmt.Sprintf(`
resource "mysql_database" "test" {
  name = "%s"
}

resource "mysql_user" "test" {
  user     = "jdoe-%s"
  host     = "example.com"
}

resource "mysql_grant" "test" {
  user       = "${mysql_user.test.user}"
  host       = "${mysql_user.test.host}"
  database   = "${mysql_database.test.name}"
  privileges = ["UPDATE", "SELECT"]
  grant      = true
}
`, dbName, dbName)
}

func testAccGrantConfig_specialChars(dbName, user, password string) string {
	return fmt.Sprintf(`
resource "mysql_database" "test" {
  name = %q
}

resource "mysql_user" "test" {
  user               = %q
  host               = "%%"
  plaintext_password = %q
}

resource "mysql_grant" "test" {
  user       = mysql_user.test.user
  host       = mysql_user.test.host
  database   = mysql_database.test.name
  privileges = ["SELECT"]
}
`, dbName, user, password)
}

// testAccCanConnectAs opens a new connection with the given credentials and
// checks that the user can list the tables of the given database.
func testAccCanConnectAs(user, password, dbName string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		cfg := testAccProvider.Meta().(*MySQLConfiguration).Config.Clone()
		cfg.User = user
		cfg.Passwd = password
		cfg.DBName = ""
		db, err := sql.Open("mysql", cfg.FormatDSN())
		if err != nil {
			return err
		}
		defer db.Close()

		rows, err := db.Query("SHOW TABLES FROM " + quoteIdentifier(dbName))
		if err != nil {
			return fmt.Errorf("connecting as %q and reading %q failed: %s", user, dbName, err)
		}
		return rows.Close()
	}
}

func TestAccGrant_role(t *testing.T) {
	dbName := fmt.Sprintf("tf-test-%d", rand.Intn(100))
	roleName := fmt.Sprintf("TFRole%d", rand.Intn(100))
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			db, err := connectToMySQL(context.Background(), testAccProvider.Meta().(*MySQLConfiguration))
			if err != nil {
				return
			}

			requiredVersion, _ := version.NewVersion("8.0.0")
			currentVersion, err := serverVersion(db)
			if err != nil {
				return
			}

			if currentVersion.LessThan(requiredVersion) {
				t.Skip("Roles require MySQL 8+")
			}
		},
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccGrantCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccGrantConfig_role(dbName, roleName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("mysql_grant.test", "role", roleName),
				),
			},
		},
	})
}

func TestAccGrant_roleToUser(t *testing.T) {
	dbName := fmt.Sprintf("tf-test-%d", rand.Intn(100))
	roleName := fmt.Sprintf("TFRole%d", rand.Intn(100))
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			testAccPreCheck(t)
			db, err := connectToMySQL(context.Background(), testAccProvider.Meta().(*MySQLConfiguration))
			if err != nil {
				return
			}

			requiredVersion, _ := version.NewVersion("8.0.0")
			currentVersion, err := serverVersion(db)
			if err != nil {
				return
			}

			if currentVersion.LessThan(requiredVersion) {
				t.Skip("Roles require MySQL 8+")
			}
		},
		ProviderFactories: testAccProviderFactories,
		CheckDestroy:      testAccGrantCheckDestroy,
		Steps: []resource.TestStep{
			{
				Config: testAccGrantConfig_roleToUser(dbName, roleName),
				Check: resource.ComposeTestCheckFunc(
					resource.TestCheckResourceAttr("mysql_grant.test", "user", fmt.Sprintf("jdoe-%s", dbName)),
					resource.TestCheckResourceAttr("mysql_grant.test", "host", "example.com"),
					resource.TestCheckResourceAttr("mysql_grant.test", "roles.#", "1"),
				),
			},
		},
	})
}

func testAccPrivilegeExists(rn string, privilege string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources[rn]
		if !ok {
			return fmt.Errorf("resource not found: %s", rn)
		}

		if rs.Primary.ID == "" {
			return fmt.Errorf("grant id not set")
		}

		db, err := connectToMySQL(context.Background(), testAccProvider.Meta().(*MySQLConfiguration))
		if err != nil {
			return err
		}

		id := strings.Split(rs.Primary.ID, ":")

		var userOrRole string
		if strings.Contains(id[0], "@") {
			parts := strings.Split(id[0], "@")
			userOrRole = fmt.Sprintf("'%s'@'%s'", parts[0], parts[1])
		} else {
			userOrRole = fmt.Sprintf("'%s'", id[0])
		}

		stmtSQL := fmt.Sprintf("SHOW GRANTS FOR %s", userOrRole)
		log.Printf("[DEBUG] SQL: %s", stmtSQL)
		rows, err := db.Query(stmtSQL)
		if err != nil {
			return fmt.Errorf("error reading grant: %s", err)
		}
		defer rows.Close()

		privilegeFound := false
		for rows.Next() {
			var grants string
			err = rows.Scan(&grants)
			if err != nil {
				return fmt.Errorf("failed to read grant for %s: %s", userOrRole, err)
			}
			log.Printf("[DEBUG] GRANTS = %s", grants)
			privIndex := strings.Index(grants, privilege)
			if privIndex != -1 {
				privilegeFound = true
			}
		}

		if !privilegeFound {
			return fmt.Errorf("grant no found for %s", userOrRole)
		}

		return nil
	}
}

func testAccGrantCheckDestroy(s *terraform.State) error {
	db, err := connectToMySQL(context.Background(), testAccProvider.Meta().(*MySQLConfiguration))
	if err != nil {
		return err
	}

	for _, rs := range s.RootModule().Resources {
		if rs.Type != "mysql_grant" {
			continue
		}

		id := strings.Split(rs.Primary.ID, ":")

		var userOrRole string
		if strings.Contains(id[0], "@") {
			parts := strings.Split(id[0], "@")
			userOrRole = fmt.Sprintf("'%s'@'%s'", parts[0], parts[1])
		} else {
			userOrRole = fmt.Sprintf("'%s'", id[0])
		}

		stmtSQL := fmt.Sprintf("SHOW GRANTS FOR %s", userOrRole)
		log.Printf("[DEBUG] SQL: %s", stmtSQL)
		rows, err := db.Query(stmtSQL)
		if err != nil {
			if mysqlErr, ok := err.(*mysql.MySQLError); ok {
				if mysqlErr.Number == nonexistingGrantErrCode {
					return nil
				}
			}

			return fmt.Errorf("error reading grant: %s", err)
		}
		defer rows.Close()

		if rows.Next() {
			return fmt.Errorf("grant still exists for: %s", userOrRole)
		}
	}
	return nil
}

func testAccGrantConfig_basic(dbName string) string {
	return fmt.Sprintf(`
resource "mysql_database" "test" {
  name = "%s"
}

resource "mysql_user" "test" {
  user     = "jdoe-%s"
  host     = "example.com"
}

resource "mysql_grant" "test" {
  user       = "${mysql_user.test.user}"
  host       = "${mysql_user.test.host}"
  database   = "${mysql_database.test.name}"
  privileges = ["UPDATE", "SELECT"]
}
`, dbName, dbName)
}

func testAccGrantConfig_ssl(dbName string) string {
	return fmt.Sprintf(`
resource "mysql_database" "test" {
  name = "%s"
}

resource "mysql_user" "test" {
  user     = "jdoe-%s"
  host     = "example.com"
}

resource "mysql_grant" "test" {
  user       = "${mysql_user.test.user}"
  host       = "${mysql_user.test.host}"
  database   = "${mysql_database.test.name}"
  privileges = ["UPDATE", "SELECT"]
  tls_option = "SSL"
}
`, dbName, dbName)
}

func testAccGrantConfig_role(dbName string, roleName string) string {
	return fmt.Sprintf(`
resource "mysql_database" "test" {
  name = "%s"
}

resource "mysql_role" "test" {
  name = "%s"
}

resource "mysql_grant" "test" {
  role       = "${mysql_role.test.name}"
  database   = "${mysql_database.test.name}"
  privileges = ["SELECT", "UPDATE"]
}
`, dbName, roleName)
}

func testAccGrantConfig_roleToUser(dbName string, roleName string) string {
	return fmt.Sprintf(`
resource "mysql_database" "test" {
  name = "%s"
}

resource "mysql_user" "jdoe" {
  user     = "jdoe-%s"
  host     = "example.com"
}

resource "mysql_role" "test" {
  name = "%s"
}

resource "mysql_grant" "test" {
  user     = "${mysql_user.jdoe.user}"
  host     = "${mysql_user.jdoe.host}"
  database = "${mysql_database.test.name}"
  roles    = ["${mysql_role.test.name}"]
}
`, dbName, dbName, roleName)
}
