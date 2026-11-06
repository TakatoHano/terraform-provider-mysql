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
		Description:   "Creates and manages a user on a MySQL server.",
		CreateContext: CreateUser,
		UpdateContext: UpdateUser,
		ReadContext:   ReadUser,
		DeleteContext: DeleteUser,
		Importer: &schema.ResourceImporter{
			StateContext: ImportUser,
		},

		Schema: map[string]*schema.Schema{
			"user": {
				Type:        schema.TypeString,
				Description: "The name of the user.",
				Required:    true,
				ForceNew:    true,
			},

			"host": {
				Type:        schema.TypeString,
				Description: "The source host of the user. Defaults to \"localhost\".",
				Optional:    true,
				ForceNew:    true,
				Default:     "localhost",
			},

			"plaintext_password": {
				Type:        schema.TypeString,
				Description: "The password for the user. This must be provided in plain text, so the data source for it must be secured. An _unsalted_ hash of the provided password is stored in state. Conflicts with `auth_plugin`.",
				Optional:    true,
				Sensitive:   true,
				StateFunc:   hashSum,
			},

			"password": {
				Type:          schema.TypeString,
				Description:   "Deprecated alias of `plaintext_password`, whose value is *stored as plaintext in state*. Prefer to use `plaintext_password` instead, which stores the password as an unsalted hash. Conflicts with `auth_plugin`.",
				Optional:      true,
				ConflictsWith: []string{"plaintext_password"},
				Sensitive:     true,
				Deprecated:    "Please use plaintext_password instead",
			},

			"auth_plugin": {
				Type:          schema.TypeString,
				Description:   "Use an authentication plugin (https://dev.mysql.com/doc/refman/5.7/en/authentication-plugins.html) to authenticate the user instead of using password authentication. Supported values are `AWSAuthenticationPlugin` (IAM authentication with Amazon Aurora) and `mysql_no_login` (the MySQL No-Login Authentication Plugin, which must be active in MySQL). Conflicts with `password` and `plaintext_password`.",
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"plaintext_password", "password"},
			},

			"tls_option": {
				Type:             schema.TypeString,
				Description:      "An TLS-Option for the `CREATE USER` or `ALTER USER` statement. The value is suffixed to `REQUIRE`. A value of 'SSL' will generate a `CREATE USER ... REQUIRE SSL` statement. See the MySQL `CREATE USER` documentation (https://dev.mysql.com/doc/refman/5.7/en/create-user.html) for more. Ignored if MySQL version is under 5.7.0. Allowed forms: `NONE`, `SSL`, `X509`, or one or more of `CIPHER '...'`, `ISSUER '...'`, `SUBJECT '...'` joined by `AND`.",
				Optional:         true,
				ForceNew:         true,
				Default:          "NONE",
				ValidateFunc:     validateTLSOption,
				DiffSuppressFunc: suppressEquivalentTLSOption,
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

	stmtSQL := "CREATE USER " + userHostLiteral(d.Get("user").(string), d.Get("host").(string))

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
		stmtSQL = stmtSQL + " IDENTIFIED BY " + quoteString(password)
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
			stmtSQL = fmt.Sprintf("SET PASSWORD FOR %s = PASSWORD(%s)",
				userHostLiteral(d.Get("user").(string), d.Get("host").(string)),
				quoteString(newpw.(string)))
		} else {
			stmtSQL = fmt.Sprintf("ALTER USER %s IDENTIFIED BY %s",
				userHostLiteral(d.Get("user").(string), d.Get("host").(string)),
				quoteString(newpw.(string)))
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
		stmtSQL := fmt.Sprintf("ALTER USER %s REQUIRE %s",
			userHostLiteral(d.Get("user").(string), d.Get("host").(string)),
			d.Get("tls_option").(string))

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

	info, err := queryUserTLSAuth(ctx, db, d.Get("user").(string), d.Get("host").(string))
	if errors.Is(err, sql.ErrNoRows) {
		d.SetId("")
		return nil
	}
	if err != nil {
		return diag.FromErr(err)
	}

	// Refresh tls_option, but keep an equivalent spelling from the config.
	if tls, ok := info.tlsOption(); ok {
		current, _ := d.Get("tls_option").(string)
		if canonicalTLSOption(current) != canonicalTLSOption(tls) {
			if err := d.Set("tls_option", tls); err != nil {
				return diag.FromErr(err)
			}
		}
	}

	return nil
}

func DeleteUser(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	stmtSQL := "DROP USER " + userHostLiteral(d.Get("user").(string), d.Get("host").(string))

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

	info, err := queryUserTLSAuth(ctx, db, user, host)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("user %s, host %s not found", user, host)
	}
	if err != nil {
		return nil, err
	}

	if err := d.Set("user", user); err != nil {
		return nil, err
	}
	if err := d.Set("host", host); err != nil {
		return nil, err
	}

	if tls, ok := info.tlsOption(); ok {
		if err := d.Set("tls_option", tls); err != nil {
			return nil, err
		}
	} else {
		log.Printf("[WARN] cannot represent the REQUIRE options of %s@%s as tls_option; leaving it unset", user, host)
	}

	// Only the plugins CreateUser understands can round-trip through auth_plugin.
	switch info.plugin {
	case "AWSAuthenticationPlugin", "mysql_no_login":
		if err := d.Set("auth_plugin", info.plugin); err != nil {
			return nil, err
		}
	}

	return []*schema.ResourceData{d}, nil
}
