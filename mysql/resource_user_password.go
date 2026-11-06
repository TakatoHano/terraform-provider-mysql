package mysql

import (
	"context"
	"fmt"

	"github.com/TakatoHano/terraform-provider-mysql/mysql/internal/encryption"
	"github.com/gofrs/uuid/v5"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func resourceUserPassword() *schema.Resource {
	return &schema.Resource{
		Description:   "Sets and manages a randomly generated password for a user on a MySQL server, encrypted with a PGP key.",
		CreateContext: SetUserPassword,
		ReadContext:   ReadUserPassword,
		DeleteContext: DeleteUserPassword,
		Schema: map[string]*schema.Schema{
			"user": {
				Type:        schema.TypeString,
				Description: "The user to set the password for.",
				Required:    true,
				ForceNew:    true,
			},
			"host": {
				Type:        schema.TypeString,
				Description: "The source host of the user. Defaults to `localhost`.",
				Optional:    true,
				ForceNew:    true,
				Default:     "localhost",
			},
			"pgp_key": {
				Type:        schema.TypeString,
				Description: "Either a base-64 encoded PGP public key, or a keybase username in the form `keybase:some_person_that_exists`.",
				ForceNew:    true,
				Required:    true,
			},
			"key_fingerprint": {
				Type:        schema.TypeString,
				Description: "The fingerprint of the PGP key used to encrypt the password.",
				Computed:    true,
			},
			"encrypted_password": {
				Type:        schema.TypeString,
				Description: "The encrypted password, base64 encoded.",
				Computed:    true,
			},
		},
	}
}

func SetUserPassword(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	uuid, err := uuid.NewV4()
	if err != nil {
		return diag.FromErr(err)
	}

	password := uuid.String()
	pgpKey := d.Get("pgp_key").(string)
	encryptionKey, err := encryption.RetrieveGPGKey(pgpKey)
	if err != nil {
		return diag.FromErr(err)
	}
	fingerprint, encrypted, err := encryption.EncryptValue(encryptionKey, password, "MySQL Password")
	if err != nil {
		return diag.FromErr(err)
	}
	d.Set("key_fingerprint", fingerprint)
	d.Set("encrypted_password", encrypted)

	requiredVersion, _ := version.NewVersion("8.0.0")
	currentVersion, err := serverVersion(db)
	if err != nil {
		return diag.FromErr(err)
	}

	passSQL := quoteString(password)
	if currentVersion.LessThan(requiredVersion) {
		passSQL = fmt.Sprintf("PASSWORD(%s)", passSQL)
	}

	sql := fmt.Sprintf("SET PASSWORD FOR %s = %s",
		userHostLiteral(d.Get("user").(string), d.Get("host").(string)),
		passSQL)

	_, err = db.ExecContext(ctx, sql)
	if err != nil {
		return diag.FromErr(err)
	}
	user := fmt.Sprintf("%s@%s",
		d.Get("user").(string),
		d.Get("host").(string))
	d.SetId(user)
	return nil
}

func ReadUserPassword(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	// This is obviously not possible.
	return nil
}

func DeleteUserPassword(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	// We don't need to do anything on the MySQL side here. Just need TF
	// to remove from the state file.
	return nil
}
