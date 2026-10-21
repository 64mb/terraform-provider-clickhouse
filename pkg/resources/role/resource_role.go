package resourcerole

import (
	"context"
	"fmt"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func ResourceRole() *schema.Resource {
	return &schema.Resource{
		Description: "Resource to manage Clickhouse roles",
		CustomizeDiff: func(_ context.Context, diff *schema.ResourceDiff, _ interface{}) error {
			if diff.HasChange("name") || diff.HasChange("database") || diff.HasChange("privileges") {
				return diff.SetNewComputed("granted_privileges")
			}
			return nil
		},
		CreateContext: resourceRoleCreate,
		ReadContext:   resourceRoleRead,
		DeleteContext: resourceRoleDelete,
		UpdateContext: resourceRoleUpdate,
		Schema: map[string]*schema.Schema{
			"name": {
				Description: "Role name",
				Type:        schema.TypeString,
				Required:    true,
			},
			"database": {
				Description: "Database where to grant permissions to the user. You can apply privileges to all databases by using '*'",
				Type:        schema.TypeString,
				Required:    true,
			},
			"granted_privileges": {
				Description: "Effective server grants recorded after the last apply, including each privilege and scope",
				Type:        schema.TypeSet,
				Computed:    true,
				Elem:        &schema.Schema{Type: schema.TypeString},
			},
			"privileges": {
				Description: "Granted privileges to the role. Privileges will be granted at DB level",
				Type:        schema.TypeSet,
				Optional:    true,
				Elem: &schema.Schema{
					Type: schema.TypeString,
				},
			},
		},
	}
}

func resourceRoleUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	var diags diag.Diagnostics

	client := meta.(*common.ApiClient)
	conn := client.ClickhouseConnection

	planRoleName := d.Get("name").(string)
	planDatabase := d.Get("database").(string)
	planPrivileges := d.Get("privileges").(*schema.Set)

	diags = ValidatePrivileges(planDatabase, planPrivileges)

	if diags.HasError() {
		return diags
	}

	chRoleService := CHRoleService{CHConnection: conn}
	chRole, err := chRoleService.UpdateRole(ctx, RoleResource{Name: planRoleName, Database: planDatabase, Privileges: planPrivileges}, d)

	if err != nil {
		return diag.FromErr(fmt.Errorf("resource role update: %w", err))
	}

	d.SetId(chRole.Name)
	if err := recordRoleGrants(d, chRole); err != nil {
		return diag.FromErr(err)
	}
	return diags
}

func resourceRoleRead(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	var diags diag.Diagnostics

	client := meta.(*common.ApiClient)
	conn := client.ClickhouseConnection
	chRoleService := CHRoleService{CHConnection: conn}

	roleNameState := d.Get("name").(string)
	chRole, err := chRoleService.GetRole(ctx, roleNameState)
	if err != nil {
		return diag.FromErr(fmt.Errorf("resource role read: %w", err))
	}

	if chRole == nil {
		d.SetId("")
		return nil
	}

	previousSnapshot := d.Get("granted_privileges").(*schema.Set)
	currentSnapshot := effectiveGrants(chRole)
	if sameRoleSnapshot(previousSnapshot, currentSnapshot) {
		if !snapshotHasRaw(previousSnapshot) && snapshotHasRaw(currentSnapshot) {
			return diag.FromErr(recordRoleGrants(d, chRole))
		}
		return nil
	}
	roleResource, err := chRole.ToRoleResource()
	if err != nil {
		return diag.FromErr(fmt.Errorf("resource role read: %w", err))
	}

	if err := d.Set("name", roleResource.Name); err != nil {
		return diag.FromErr(fmt.Errorf("resource role read: %w", err))
	}

	if roleResource.Database == "" {
		roleResource.Database = d.Get("database").(string)
	}
	if err := chRoleService.refreshRoleState(ctx, d, chRole, roleResource); err != nil {
		return diag.FromErr(err)
	}

	d.SetId(roleResource.Name)

	return diags
}

func resourceRoleCreate(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	var diags diag.Diagnostics
	client := meta.(*common.ApiClient)
	conn := client.ClickhouseConnection

	database := d.Get("database").(string)
	roleName := d.Get("name").(string)
	privileges := d.Get("privileges").(*schema.Set)

	diags = ValidatePrivileges(database, privileges)
	if diags.HasError() {
		return diags
	}

	chRoleService := CHRoleService{CHConnection: conn}
	chRole, err := chRoleService.CreateRole(ctx, roleName, database, common.StringSetToList(privileges))

	if err != nil {
		return diag.FromErr(fmt.Errorf("resource role create: %w", err))
	}

	d.SetId(chRole.Name)
	if err := recordRoleGrants(d, chRole); err != nil {
		return diag.FromErr(err)
	}
	return diags
}

func resourceRoleDelete(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
	var diags diag.Diagnostics
	client := meta.(*common.ApiClient)
	conn := client.ClickhouseConnection

	roleName := d.Get("name").(string)
	chRoleService := CHRoleService{CHConnection: conn}

	if err := chRoleService.DeleteRole(ctx, roleName); err != nil {
		return diag.FromErr(fmt.Errorf("resource role delete: %w", err))
	}
	return diags
}
