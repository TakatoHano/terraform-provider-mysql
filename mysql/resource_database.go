package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

const defaultCharacterSetKeyword = "CHARACTER SET "
const defaultCollateKeyword = "COLLATE "
const unknownDatabaseErrCode = 1049

func resourceDatabase() *schema.Resource {
	return &schema.Resource{
		CreateContext: CreateDatabase,
		UpdateContext: UpdateDatabase,
		ReadContext:   ReadDatabase,
		DeleteContext: DeleteDatabase,
		Importer: &schema.ResourceImporter{
			StateContext: schema.ImportStatePassthroughContext,
		},
		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"default_character_set": {
				Type:     schema.TypeString,
				Optional: true,
				Default:  "utf8",

				DiffSuppressFunc: suppressUTF8Alias,
			},

			"default_collation": {
				Type:     schema.TypeString,
				Optional: true,
				Default:  "utf8_general_ci",

				DiffSuppressFunc: suppressUTF8Alias,
			},
		},
	}
}

// suppressUTF8Alias treats utf8 and utf8mb3 (and their collations) as equal:
// MySQL 8.0.30+ reports the legacy "utf8" alias as "utf8mb3".
func suppressUTF8Alias(k, old, new string, d *schema.ResourceData) bool {
	norm := func(v string) string {
		if strings.HasPrefix(v, "utf8mb3") {
			return "utf8" + strings.TrimPrefix(v, "utf8mb3")
		}
		return v
	}
	return norm(old) == norm(new)
}

func CreateDatabase(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	stmtSQL := databaseConfigSQL("CREATE", d)
	log.Println("Executing statement:", stmtSQL)

	_, err = db.ExecContext(ctx, stmtSQL)
	if err != nil {
		return diag.FromErr(err)
	}

	d.SetId(d.Get("name").(string))

	return ReadDatabase(ctx, d, meta)
}

func UpdateDatabase(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	stmtSQL := databaseConfigSQL("ALTER", d)
	log.Println("Executing statement:", stmtSQL)

	_, err = db.ExecContext(ctx, stmtSQL)
	if err != nil {
		return diag.FromErr(err)
	}

	return ReadDatabase(ctx, d, meta)
}

func ReadDatabase(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	// This is kinda flimsy-feeling, since it depends on the formatting
	// of the SHOW CREATE DATABASE output... but this data doesn't seem
	// to be available any other way, so hopefully MySQL keeps this
	// compatible in future releases.

	name := d.Id()
	stmtSQL := "SHOW CREATE DATABASE " + quoteIdentifier(name)

	log.Println("Executing query:", stmtSQL)
	var createSQL, _database string
	err = db.QueryRowContext(ctx, stmtSQL).Scan(&_database, &createSQL)
	if err != nil {
		if mysqlErr, ok := err.(*mysql.MySQLError); ok {
			if mysqlErr.Number == unknownDatabaseErrCode {
				d.SetId("")
				return nil
			}
		}
		return diag.Errorf("error during show create database: %s", err)
	}

	defaultCharset := extractIdentAfter(createSQL, defaultCharacterSetKeyword)
	defaultCollation := extractIdentAfter(createSQL, defaultCollateKeyword)

	if defaultCollation == "" && defaultCharset != "" {
		// MySQL doesn't return the collation if it's the default one for
		// the charset, so if we don't have a collation we need to go
		// hunt for the default.
		stmtSQL := "SHOW COLLATION WHERE `Charset` = ? AND `Default` = 'Yes'"
		var empty interface{}

		requiredVersion, _ := version.NewVersion("8.0.0")
		currentVersion, err := serverVersion(db)
		if err != nil {
			return diag.FromErr(err)
		}

		serverVersionString, err := serverVersionString(db)
		if err != nil {
			return diag.FromErr(err)
		}

		// MySQL 8 returns more data in a row.
		var res error
		if !strings.Contains(serverVersionString, "MariaDB") && currentVersion.GreaterThan(requiredVersion) {
			res = db.QueryRowContext(ctx, stmtSQL, defaultCharset).Scan(&defaultCollation, &empty, &empty, &empty, &empty, &empty, &empty)
		} else {
			res = db.QueryRowContext(ctx, stmtSQL, defaultCharset).Scan(&defaultCollation, &empty, &empty, &empty, &empty, &empty)
		}

		if res != nil {
			if res == sql.ErrNoRows {
				return diag.Errorf("charset %s has no default collation", defaultCharset)
			}

			return diag.Errorf("error getting default charset: %s, %s", res, defaultCharset)
		}
	}

	d.Set("name", name)
	d.Set("default_character_set", defaultCharset)
	d.Set("default_collation", defaultCollation)

	return nil
}

func DeleteDatabase(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	name := d.Id()
	stmtSQL := "DROP DATABASE " + quoteIdentifier(name)
	log.Println("Executing statement:", stmtSQL)

	_, err = db.ExecContext(ctx, stmtSQL)
	if err == nil {
		d.SetId("")
	}
	return diag.FromErr(err)
}

func databaseConfigSQL(verb string, d *schema.ResourceData) string {
	name := d.Get("name").(string)
	defaultCharset := d.Get("default_character_set").(string)
	defaultCollation := d.Get("default_collation").(string)

	var defaultCharsetClause string
	var defaultCollationClause string

	if defaultCharset != "" {
		defaultCharsetClause = defaultCharacterSetKeyword + quoteIdentifier(defaultCharset)
	}
	if defaultCollation != "" {
		defaultCollationClause = defaultCollateKeyword + quoteIdentifier(defaultCollation)
	}

	return fmt.Sprintf(
		"%s DATABASE %s %s %s",
		verb,
		quoteIdentifier(name),
		defaultCharsetClause,
		defaultCollationClause,
	)
}

func extractIdentAfter(sql string, keyword string) string {
	charsetIndex := strings.Index(sql, keyword)
	if charsetIndex != -1 {
		charsetIndex += len(keyword)
		remain := sql[charsetIndex:]
		spaceIndex := strings.IndexRune(remain, ' ')
		return remain[:spaceIndex]
	}

	return ""
}
