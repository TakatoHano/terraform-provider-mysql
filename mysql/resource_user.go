package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"errors"

	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func resourceUser() *schema.Resource {
	return &schema.Resource{
		CreateContext: CreateUser,
		UpdateContext: UpdateUser,
		ReadContext:   ReadUser,
		DeleteContext: DeleteUser,
		Importer: &schema.ResourceImporter{
			StateContext: ImportUser,
		},

		Schema: map[string]*schema.Schema{
			"user": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"host": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Default:  "localhost",
			},

			"plaintext_password": {
				Type:      schema.TypeString,
				Optional:  true,
				Sensitive: true,
				StateFunc: hashSum,
			},

			"password": {
				Type:          schema.TypeString,
				Optional:      true,
				ConflictsWith: []string{"plaintext_password"},
				Sensitive:     true,
				Deprecated:    "Please use plaintext_password instead",
			},

			"auth_plugin": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"plaintext_password", "password"},
			},

			"tls_option": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Default:  "NONE",
			},
		},
	}
}

func CreateUser(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	var authStm string
	var auth string
	if v, ok := d.GetOk("auth_plugin"); ok {
		auth = v.(string)
	}

	if len(auth) > 0 {
		switch auth {
		case "AWSAuthenticationPlugin":
			authStm = " IDENTIFIED WITH AWSAuthenticationPlugin as 'RDS'"
		case "mysql_no_login":
			authStm = " IDENTIFIED WITH mysql_no_login"
		}
	}

	stmtSQL := fmt.Sprintf("CREATE USER '%s'@'%s'",
		d.Get("user").(string),
		d.Get("host").(string))

	var password string
	if v, ok := d.GetOk("plaintext_password"); ok {
		password = v.(string)
	} else {
		password = d.Get("password").(string)
	}

	if auth == "AWSAuthenticationPlugin" && d.Get("host").(string) == "localhost" {
		return diag.Errorf("cannot use IAM auth against localhost")
	}

	if authStm != "" {
		stmtSQL = stmtSQL + authStm
	} else {
		stmtSQL = stmtSQL + fmt.Sprintf(" IDENTIFIED BY '%s'", password)
	}

	requiredVersion, _ := version.NewVersion("5.7.0")
	currentVersion, err := serverVersion(db)
	if err != nil {
		return diag.FromErr(err)
	}

	if currentVersion.GreaterThan(requiredVersion) && d.Get("tls_option").(string) != "" {
		stmtSQL += fmt.Sprintf(" REQUIRE %s", d.Get("tls_option").(string))
	}

	log.Println("Executing statement:", stmtSQL)
	_, err = db.ExecContext(ctx, stmtSQL)
	if err != nil {
		return diag.FromErr(err)
	}

	user := fmt.Sprintf("%s@%s", d.Get("user").(string), d.Get("host").(string))
	d.SetId(user)

	return nil
}

func UpdateUser(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	var auth string
	if v, ok := d.GetOk("auth_plugin"); ok {
		auth = v.(string)
	}

	if len(auth) > 0 {
		// nothing to change, return
		return nil
	}

	var newpw interface{}
	if d.HasChange("plaintext_password") {
		_, newpw = d.GetChange("plaintext_password")
	} else if d.HasChange("password") {
		_, newpw = d.GetChange("password")
	} else {
		newpw = nil
	}

	if newpw != nil {
		var stmtSQL string

		/* ALTER USER syntax introduced in MySQL 5.7.6 deprecates SET PASSWORD (GH-8230) */
		serverVersion, err := serverVersion(db)
		if err != nil {
			return diag.Errorf("could not determine server version: %s", err)
		}

		ver, _ := version.NewVersion("5.7.6")
		if serverVersion.LessThan(ver) {
			stmtSQL = fmt.Sprintf("SET PASSWORD FOR '%s'@'%s' = PASSWORD('%s')",
				d.Get("user").(string),
				d.Get("host").(string),
				newpw.(string))
		} else {
			stmtSQL = fmt.Sprintf("ALTER USER '%s'@'%s' IDENTIFIED BY '%s'",
				d.Get("user").(string),
				d.Get("host").(string),
				newpw.(string))
		}

		log.Println("Executing query:", stmtSQL)
		_, err = db.ExecContext(ctx, stmtSQL)
		if err != nil {
			return diag.FromErr(err)
		}
	}

	requiredVersion, _ := version.NewVersion("5.7.0")
	currentVersion, err := serverVersion(db)
	if err != nil {
		return diag.FromErr(err)
	}

	if d.HasChange("tls_option") && currentVersion.GreaterThan(requiredVersion) {
		stmtSQL := fmt.Sprintf("ALTER USER '%s'@'%s'  REQUIRE %s",
			d.Get("user").(string),
			d.Get("host").(string),
			fmt.Sprintf(" REQUIRE %s", d.Get("tls_option").(string)))

		log.Println("Executing query:", stmtSQL)
		_, err := db.ExecContext(ctx, stmtSQL)
		if err != nil {
			return diag.FromErr(err)
		}
	}

	return nil
}

func ReadUser(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	var exists int
	err = db.QueryRowContext(ctx, "SELECT 1 FROM mysql.user WHERE user = ? AND host = ?",
		d.Get("user").(string), d.Get("host").(string)).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		d.SetId("")
		return nil
	}
	return diag.FromErr(err)
}

func DeleteUser(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	stmtSQL := fmt.Sprintf("DROP USER '%s'@'%s'",
		d.Get("user").(string),
		d.Get("host").(string))

	log.Println("Executing statement:", stmtSQL)

	_, err = db.ExecContext(ctx, stmtSQL)
	if err == nil {
		d.SetId("")
	}
	return diag.FromErr(err)
}

func ImportUser(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	// terraform import mysql_user.user user@host
	userHost := strings.SplitN(d.Id(), "@", 2)

	if len(userHost) != 2 {
		return nil, fmt.Errorf("wrong ID format %s (expected USER@HOST)", d.Id())
	}

	user := userHost[0]
	host := userHost[1]

	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))

	if err != nil {
		return nil, err
	}

	var count int
	err = db.QueryRowContext(ctx, "SELECT COUNT(1) FROM mysql.user WHERE user = ? AND host = ?", user, host).Scan(&count)

	if err != nil {
		return nil, err
	}

	if count == 0 {
		return nil, fmt.Errorf("user %s, host %s not found", user, host)
	}

	d.Set("user", user)
	d.Set("host", host)
	// importing tls_option is complicated
	d.Set("tls_option", "NONE")

	return []*schema.ResourceData{d}, nil
}
