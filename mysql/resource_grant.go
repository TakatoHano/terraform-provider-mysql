package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/hashicorp/go-version"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

const (
	nonexistingGrantErrCode = 1141
	// ER_PASSWORD_NO_MATCH: SHOW GRANTS for a user that does not exist on some versions.
	noMatchingUserErrCode = 1133
)

// hashPrivilege is the set hash function of mysql_grant.privileges.
func hashPrivilege(v interface{}) int {
	return schema.HashString(normalizePrivilege(v.(string)))
}

// suppressEquivalentPrivilege hides diffs between spellings of the same
// privilege, most importantly ALL vs ALL PRIVILEGES (MySQL always reports the latter).
func suppressEquivalentPrivilege(_, oldV, newV string, _ *schema.ResourceData) bool {
	return oldV != "" && newV != "" && normalizePrivilege(oldV) == normalizePrivilege(newV)
}

func resourceGrant() *schema.Resource {
	return &schema.Resource{
		CreateContext: CreateGrant,
		UpdateContext: nil,
		ReadContext:   ReadGrant,
		DeleteContext: DeleteGrant,
		Importer: &schema.ResourceImporter{
			StateContext: ImportGrant,
		},

		Schema: map[string]*schema.Schema{
			"user": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"role"},
			},

			"role": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"user", "host"},
			},

			"host": {
				Type:          schema.TypeString,
				Optional:      true,
				ForceNew:      true,
				Default:       "localhost",
				ConflictsWith: []string{"role"},
			},

			"database": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},

			"table": {
				Type:     schema.TypeString,
				Optional: true,
				ForceNew: true,
				Default:  "*",
			},

			"privileges": {
				Type:     schema.TypeSet,
				Optional: true,
				ForceNew: true,
				Elem: &schema.Schema{
					Type:             schema.TypeString,
					ValidateFunc:     validatePrivilege,
					DiffSuppressFunc: suppressEquivalentPrivilege,
				},
				// Hash the canonical form so that ALL and ALL PRIVILEGES (and
				// differently cased or spaced privileges) land in the same slot.
				Set: hashPrivilege,
			},

			"roles": {
				Type:          schema.TypeSet,
				Optional:      true,
				ForceNew:      true,
				ConflictsWith: []string{"privileges"},
				Elem:          &schema.Schema{Type: schema.TypeString},
				Set:           schema.HashString,
			},

			"grant": {
				Type:     schema.TypeBool,
				Optional: true,
				ForceNew: true,
				Default:  false,
			},

			"tls_option": {
				Type:         schema.TypeString,
				Optional:     true,
				ForceNew:     true,
				Default:      "NONE",
				ValidateFunc: validateTLSOption,
			},
		},
	}
}

// flattenQuoted joins values as comma-separated, escaped string literals.
func flattenQuoted(list []interface{}) string {
	var result []string
	for _, v := range list {
		result = append(result, quoteString(v.(string)))
	}

	return strings.Join(result, ", ")
}

func flattenList(list []interface{}, template string) string {
	var result []string
	for _, v := range list {
		result = append(result, fmt.Sprintf(template, v.(string)))
	}

	return strings.Join(result, ", ")
}

func userOrRole(user string, host string, role string, hasRoles bool) (string, bool, error) {
	if len(user) > 0 && len(host) > 0 {
		return userHostLiteral(user, host), false, nil
	} else if len(role) > 0 {
		if !hasRoles {
			return "", false, fmt.Errorf("roles are only supported on MySQL 8 and above")
		}

		return quoteString(role), true, nil
	} else {
		return "", false, fmt.Errorf("user with host or a role is required")
	}
}

func supportsRoles(db *sql.DB) (bool, error) {
	currentVersion, err := serverVersion(db)
	if err != nil {
		return false, err
	}

	requiredVersion, _ := version.NewVersion("8.0.0")
	hasRoles := currentVersion.GreaterThanOrEqual(requiredVersion)
	return hasRoles, nil
}

func CreateGrant(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	hasRoles, err := supportsRoles(db)
	if err != nil {
		return diag.FromErr(err)
	}

	var (
		privilegesOrRoles string
		grantOn           string
	)

	hasPrivs := false
	rolesGranted := 0
	if attr, ok := d.GetOk("privileges"); ok {
		privilegesOrRoles = flattenList(attr.(*schema.Set).List(), "%s")
		hasPrivs = true
	} else if attr, ok := d.GetOk("roles"); ok {
		if !hasRoles {
			return diag.Errorf("roles are only supported on MySQL 8 and above")
		}
		listOfRoles := attr.(*schema.Set).List()
		rolesGranted = len(listOfRoles)
		privilegesOrRoles = flattenQuoted(listOfRoles)
	} else {
		return diag.Errorf("one of privileges or roles is required")
	}

	user := d.Get("user").(string)
	host := d.Get("host").(string)
	role := d.Get("role").(string)

	userOrRole, isRole, err := userOrRole(user, host, role, hasRoles)
	if err != nil {
		return diag.FromErr(err)
	}

	database := formatDatabaseName(d.Get("database").(string))

	table := formatTableName(d.Get("table").(string))

	if (!isRole || hasPrivs) && rolesGranted == 0 {
		grantOn = fmt.Sprintf(" ON %s.%s", database, table)
	}

	stmtSQL := fmt.Sprintf("GRANT %s%s TO %s",
		privilegesOrRoles,
		grantOn,
		userOrRole)

	// MySQL 8+ doesn't allow REQUIRE on a GRANT statement.
	if !hasRoles && d.Get("tls_option").(string) != "" {
		stmtSQL += fmt.Sprintf(" REQUIRE %s", d.Get("tls_option").(string))
	}

	if !isRole && d.Get("grant").(bool) {
		stmtSQL += " WITH GRANT OPTION"
	}

	log.Println("Executing statement:", stmtSQL)
	_, err = db.ExecContext(ctx, stmtSQL)
	if err != nil {
		return diag.Errorf("error running SQL (%s): %s", stmtSQL, err)
	}

	id := fmt.Sprintf("%s@%s:%s", user, host, database)
	if isRole {
		id = fmt.Sprintf("%s:%s", role, database)
	}

	d.SetId(id)

	return ReadGrant(ctx, d, meta)
}

func ReadGrant(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	hasRoles, err := supportsRoles(db)
	if err != nil {
		return diag.FromErr(err)
	}

	userOrRole, _, err := userOrRole(
		d.Get("user").(string),
		d.Get("host").(string),
		d.Get("role").(string),
		hasRoles)
	if err != nil {
		return diag.FromErr(err)
	}

	entries, err := showGrants(ctx, db, userOrRole)
	if err != nil {
		if isMissingGrantError(err) {
			log.Printf("[WARN] GRANT not found for %s - removing from state", userOrRole)
			d.SetId("")
			return nil
		}
		return diag.Errorf("error reading grants for %s: %s", userOrRole, err)
	}

	// Role grants: SHOW GRANTS does not attach a database to them, so only the
	// roles themselves are compared. Roles granted by other means are ignored;
	// configured roles that are no longer granted are dropped from state.
	if current, ok := d.Get("roles").(*schema.Set); ok && current.Len() > 0 {
		granted := map[string]bool{}
		for _, e := range entries {
			for _, r := range e.Roles {
				granted[r] = true
			}
		}
		var kept []string
		for _, r := range current.List() {
			if granted[r.(string)] {
				kept = append(kept, r.(string))
			}
		}
		if len(kept) == 0 {
			log.Printf("[WARN] roles of %s not found - removing from state", userOrRole)
			d.SetId("")
			return nil
		}
		if err := d.Set("roles", kept); err != nil {
			return diag.FromErr(err)
		}
		return nil
	}

	entry, ok := findGrantEntry(entries, d.Get("database").(string), d.Get("table").(string))
	if !ok {
		log.Printf("[WARN] no grant on %s.%s for %s - removing from state",
			d.Get("database").(string), d.Get("table").(string), userOrRole)
		d.SetId("")
		return nil
	}

	privileges := entry.Privileges
	if isImpliedAllPrivileges(d, entry) {
		// MySQL 8 spells ALL on *.* out as a list of every static privilege.
		privileges = []string{"ALL PRIVILEGES"}
	}
	if err := d.Set("privileges", privileges); err != nil {
		return diag.FromErr(err)
	}
	if err := d.Set("grant", entry.GrantOption); err != nil {
		return diag.FromErr(err)
	}

	return nil
}

// isMissingGrantError reports whether err means the user or role has no grants
// (or does not exist at all).
func isMissingGrantError(err error) bool {
	var me *mysql.MySQLError
	return errors.As(err, &me) && (me.Number == nonexistingGrantErrCode || me.Number == noMatchingUserErrCode)
}

// showGrants runs SHOW GRANTS FOR grantee (an already quoted user or role) and
// parses the result.
func showGrants(ctx context.Context, db *sql.DB, grantee string) ([]grantEntry, error) {
	query := "SHOW GRANTS FOR " + grantee
	log.Println("[DEBUG] SQL:", query)

	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return parseShowGrants(lines), nil
}

func normalizeGrantTable(table string) string {
	table = unwrapBackticks(table)
	if table == "" {
		return "*"
	}
	return table
}

// findGrantEntry returns the privileges on database and table. MySQL 8 may
// split one target over several lines (static privileges, then dynamic ones
// such as APPLICATION_PASSWORD_ADMIN), so all matching lines are merged.
func findGrantEntry(entries []grantEntry, database, table string) (grantEntry, bool) {
	database = unwrapBackticks(database)
	table = normalizeGrantTable(table)
	var merged grantEntry
	found := false
	for _, e := range entries {
		if len(e.Roles) > 0 {
			continue
		}
		if e.Database == database && normalizeGrantTable(e.Table) == table {
			if !found {
				merged = grantEntry{Database: e.Database, Table: e.Table}
				found = true
			}
			merged.Privileges = append(merged.Privileges, e.Privileges...)
			merged.GrantOption = merged.GrantOption || e.GrantOption
		}
	}
	return merged, found
}

// isImpliedAllPrivileges detects the MySQL 8 behaviour of listing every static
// privilege for ALL on *.* instead of "ALL PRIVILEGES".
func isImpliedAllPrivileges(d *schema.ResourceData, entry grantEntry) bool {
	if entry.Database != "*" || normalizeGrantTable(entry.Table) != "*" {
		return false
	}
	current, ok := d.Get("privileges").(*schema.Set)
	if !ok || current.Len() != 1 || normalizePrivilege(current.List()[0].(string)) != "ALL PRIVILEGES" {
		return false
	}
	have := map[string]bool{}
	for _, p := range entry.Privileges {
		have[p] = true
	}
	if have["ALL PRIVILEGES"] {
		return false
	}
	for _, p := range []string{"SELECT", "INSERT", "UPDATE", "DELETE", "CREATE", "DROP", "ALTER", "INDEX"} {
		if !have[p] {
			return false
		}
	}
	return true
}

func DeleteGrant(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	database := formatDatabaseName(d.Get("database").(string))

	table := formatTableName(d.Get("table").(string))

	hasRoles, err := supportsRoles(db)
	if err != nil {
		return diag.FromErr(err)
	}

	userOrRole, isRole, err := userOrRole(
		d.Get("user").(string),
		d.Get("host").(string),
		d.Get("role").(string),
		hasRoles)
	if err != nil {
		return diag.FromErr(err)
	}

	roles := d.Get("roles").(*schema.Set)
	privileges := d.Get("privileges").(*schema.Set)

	var sql string
	if !isRole && len(roles.List()) == 0 {
		sql = fmt.Sprintf("REVOKE GRANT OPTION ON %s.%s FROM %s",
			database,
			table,
			userOrRole)

		log.Printf("[DEBUG] SQL: %s", sql)
		_, err = db.ExecContext(ctx, sql)
		// MySQL 8.4 answers 1141 when the user does not hold GRANT OPTION
		// (older versions silently succeed); there is nothing to revoke then.
		if me, ok := err.(*mysql.MySQLError); err != nil && !(ok && me.Number == nonexistingGrantErrCode) {
			return diag.Errorf("error revoking GRANT (%s): %s", sql, err)
		}
	}

	whatToRevoke := fmt.Sprintf("ALL ON %s.%s", database, table)
	if len(roles.List()) > 0 {
		whatToRevoke = flattenQuoted(roles.List())
	} else if len(privileges.List()) > 0 {
		privilegeList := flattenList(privileges.List(), "%s")
		whatToRevoke = fmt.Sprintf("%s ON %s.%s", privilegeList, database, table)
	}

	sql = fmt.Sprintf("REVOKE %s FROM %s", whatToRevoke, userOrRole)
	log.Printf("[DEBUG] SQL: %s", sql)
	_, err = db.ExecContext(ctx, sql)
	if err != nil {
		return diag.Errorf("error revoking ALL (%s): %s", sql, err)
	}

	return nil
}

func ImportGrant(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	// terraform import mysql_grant.user user@host:database
	// terraform import mysql_grant.role role:database
	id := d.Id()

	var user, host, role, database, grantee string
	if strings.Contains(id, "@") {
		userHostDB := strings.SplitN(id, "@", 2)
		hostDB := strings.SplitN(userHostDB[1], ":", 2)
		if len(hostDB) != 2 {
			return nil, fmt.Errorf("wrong ID format %s (expected USER@HOST:DATABASE or ROLE:DATABASE)", id)
		}
		user, host, database = userHostDB[0], hostDB[0], hostDB[1]
		grantee = userHostLiteral(user, host)
	} else {
		roleDB := strings.SplitN(id, ":", 2)
		if len(roleDB) != 2 {
			return nil, fmt.Errorf("wrong ID format %s (expected USER@HOST:DATABASE or ROLE:DATABASE)", id)
		}
		role, database = roleDB[0], roleDB[1]
		grantee = quoteString(role)
	}
	database = unwrapBackticks(database)

	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return nil, err
	}

	entries, err := showGrants(ctx, db, grantee)
	if err != nil {
		return nil, err
	}

	if role != "" {
		// Mirror the schema default so that the imported state matches a config
		// that only sets role.
		host = "localhost"
		_ = d.Set("role", role)
	} else {
		_ = d.Set("user", user)
	}
	_ = d.Set("host", host)
	_ = d.Set("database", database)
	_ = d.Set("tls_option", "NONE")

	if entry, ok := pickImportEntry(entries, database); ok {
		_ = d.Set("table", normalizeGrantTable(entry.Table))
		_ = d.Set("privileges", entry.Privileges)
		_ = d.Set("grant", entry.GrantOption)
		return []*schema.ResourceData{d}, nil
	}

	// No privileges on that database: fall back to roles granted to the user.
	if role == "" {
		var roles []string
		for _, e := range entries {
			roles = append(roles, e.Roles...)
		}
		if len(roles) > 0 {
			_ = d.Set("table", "*")
			_ = d.Set("roles", roles)
			return []*schema.ResourceData{d}, nil
		}
	}

	return nil, fmt.Errorf("grant of %s on database %s not found", grantee, database)
}

// pickImportEntry prefers the database-wide (table "*") entry of database and
// falls back to the first entry for any of its tables.
func pickImportEntry(entries []grantEntry, database string) (grantEntry, bool) {
	var first *grantEntry
	for i, e := range entries {
		if len(e.Roles) > 0 || e.Database != database {
			continue
		}
		if normalizeGrantTable(e.Table) == "*" {
			return e, true
		}
		if first == nil {
			first = &entries[i]
		}
	}
	if first != nil {
		return *first, true
	}
	return grantEntry{}, false
}
