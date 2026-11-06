package mysql

import (
	"context"
	"log"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func resourceRole() *schema.Resource {
	return &schema.Resource{
		CreateContext: CreateRole,
		ReadContext:   ReadRole,
		DeleteContext: DeleteRole,

		Schema: map[string]*schema.Schema{
			"name": {
				Type:     schema.TypeString,
				Required: true,
				ForceNew: true,
			},
		},
	}
}

func CreateRole(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	roleName := d.Get("name").(string)

	sql := "CREATE ROLE " + quoteString(roleName)
	log.Printf("[DEBUG] SQL: %s", sql)

	_, err = db.ExecContext(ctx, sql)
	if err != nil {
		return diag.Errorf("error creating role: %s", err)
	}

	d.SetId(roleName)

	return nil
}

func ReadRole(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	sql := "SHOW GRANTS FOR " + quoteString(d.Id())
	log.Printf("[DEBUG] SQL: %s", sql)

	_, err = db.ExecContext(ctx, sql)
	if err != nil {
		log.Printf("[WARN] Role (%s) not found; removing from state", d.Id())
		d.SetId("")
		return nil
	}

	d.Set("name", d.Id())

	return nil
}

func DeleteRole(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	db, err := connectToMySQL(ctx, meta.(*MySQLConfiguration))
	if err != nil {
		return diag.FromErr(err)
	}

	sql := "DROP ROLE " + quoteString(d.Get("name").(string))
	log.Printf("[DEBUG] SQL: %s", sql)

	_, err = db.ExecContext(ctx, sql)
	if err != nil {
		return diag.FromErr(err)
	}

	return nil
}
