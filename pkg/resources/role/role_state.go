package resourcerole

import (
	"context"
	"fmt"
	"strings"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func effectiveGrants(role *CHRole) *schema.Set {
	var grants []string
	for _, privilege := range role.EffectivePrivileges {
		grants = append(grants, "system.grants: "+privilege)
	}

	for _, grant := range role.Privileges {
		scope := grant.Scope
		if scope == "" {
			scope = roleScope(grant.Database)
		}
		grants = append(grants, "SHOW GRANTS: "+grant.AccessType+" ON "+scope)
	}
	if len(grants) == 0 {
		grants = []string{"NONE"}
	}
	return common.StringListToSet(grants)
}

func recordRoleGrants(data *schema.ResourceData, role *CHRole) error {
	if err := data.Set("granted_privileges", effectiveGrants(role)); err != nil {
		return fmt.Errorf("record effective role privileges: %w", err)
	}
	return nil
}

func (rs *CHRoleService) refreshRoleState(ctx context.Context, data *schema.ResourceData, role *CHRole, actual *RoleResource) error {
	desired := data.Get("privileges").(*schema.Set)
	snapshot := data.Get("granted_privileges").(*schema.Set)
	current := effectiveGrants(role)
	preserve := sameRoleSnapshot(snapshot, current) && data.Get("database").(string) == actual.Database
	if snapshot.Len() == 0 && !preserve {
		catalog, err := rs.privilegeCatalog(ctx)
		if err != nil {
			return err
		}
		preserve = data.Get("database").(string) == actual.Database && catalog.equivalent(common.StringSetToList(desired), role.Privileges, actual.Database)
	}
	if !preserve {
		if snapshot.Len() > 0 && sameStringSet(desired, actual.Privileges) && len(role.RawPrivileges) > 0 {
			actual.Privileges = common.StringListToSet(role.RawPrivileges)
		}
		if err := data.Set("database", actual.Database); err != nil {
			return err
		}
		if err := data.Set("privileges", actual.Privileges); err != nil {
			return err
		}
	}
	return recordRoleGrants(data, role)
}

func (catalog privilegeCatalog) leaves(name, database string, output map[string]bool) {
	definition, ok := catalog[strings.ToUpper(name)]
	if !ok {
		output[strings.ToUpper(name)] = true
		return
	}
	found := false
	for key, child := range catalog {
		if strings.ToUpper(child.Name) != key || child.Parent != definition.Name {
			continue
		}
		found = true
		catalog.leaves(child.Name, database, output)
	}
	if !found && (database == "*" || definition.Level == "DATABASE" || definition.Level == "TABLE" || definition.Level == "COLUMN" || definition.Level == "VIEW" || definition.Level == "DICTIONARY") {
		output[strings.ToUpper(definition.Name)] = true
	}
}

func (catalog privilegeCatalog) equivalent(desired []string, actual []CHGrant, database string) bool {
	wanted, observed := map[string]bool{}, map[string]bool{}
	for _, privilege := range desired {
		catalog.leaves(privilege, database, wanted)
	}
	for _, grant := range actual {
		catalog.leaves(grant.AccessType, database, observed)
	}
	if len(wanted) != len(observed) {
		return false
	}
	for privilege := range wanted {
		if !observed[privilege] {
			return false
		}
	}
	return true
}

func sameStringSet(left, right *schema.Set) bool {
	if left.Len() != right.Len() {
		return false
	}
	values := map[string]bool{}
	for _, value := range left.List() {
		values[value.(string)] = true
	}
	for _, value := range right.List() {
		if !values[value.(string)] {
			return false
		}
	}
	return true
}

func snapshotHasRaw(snapshot *schema.Set) bool {
	for _, value := range snapshot.List() {
		if strings.HasPrefix(value.(string), "system.grants: ") {
			return true
		}
	}
	return false
}

func sameRoleSnapshot(previous, current *schema.Set) bool {
	if snapshotHasRaw(previous) && snapshotHasRaw(current) {
		return sameStringSet(previous, current)
	}
	compact := func(snapshot *schema.Set) *schema.Set {
		var values []string
		for _, value := range snapshot.List() {
			if !strings.HasPrefix(value.(string), "system.grants: ") {
				values = append(values, value.(string))
			}
		}
		return common.StringListToSet(values)
	}
	return sameStringSet(compact(previous), compact(current))
}
