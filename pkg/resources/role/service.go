package resourcerole

import (
	"context"
	"fmt"
	"strings"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

type CHRoleService struct{ CHConnection *driver.Conn }

// read [SHOW GRANTS] to retain compact source privileges such as [REMOTE]
func (rs *CHRoleService) getRoleGrants(ctx context.Context, roleName string) (privileges []CHGrant, err error) {
	query := fmt.Sprintf("SHOW GRANTS FOR %s", quoteRoleIdentifier(roleName))
	rows, err := (*rs.CHConnection).Query(ctx, query)

	if err != nil {
		return nil, fmt.Errorf("error fetching role grants: %w", err)
	}

	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			privileges = nil
			err = fmt.Errorf("close role grants result: %w", closeErr)
		}
	}()
	for rows.Next() {
		var grantStatement string
		err := rows.Scan(&grantStatement)
		if err != nil {
			return nil, fmt.Errorf("error scanning grant statement: %w", err)
		}

		// partial revokes remain outside the database-level role contract
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(grantStatement)), "REVOKE ") {
			continue
		}

		grants, err := parseGrantStatement(grantStatement, roleName)
		if err != nil {
			return nil, fmt.Errorf("error parsing grant statement '%s': %w", grantStatement, err)
		}
		privileges = append(privileges, grants...)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read role grants: %w", err)
	}
	return privileges, nil
}

func parseGrantStatement(statement, expectedRole string) ([]CHGrant, error) {
	stmt := strings.TrimSpace(statement)

	upper := strings.ToUpper(stmt)

	if !strings.HasPrefix(upper, "GRANT ") {
		return nil, fmt.Errorf("invalid grant statement (missing GRANT): %q", statement)
	}

	grantBody := stmt[len("GRANT "):]

	onIdx := keywordOutsideQuotes(grantBody, " ON ")
	if onIdx == -1 {
		return nil, fmt.Errorf("missing ' ON ' in grant statement: %s", statement)
	}

	privPart := strings.TrimSpace(grantBody[:onIdx])
	rest := grantBody[onIdx+4:]

	toIdx := keywordOutsideQuotes(rest, " TO ")
	if toIdx == -1 {
		return nil, fmt.Errorf("missing ' TO ' in grant statement: %s", statement)
	}

	scopePart := strings.TrimSpace(rest[:toIdx])
	rolePart := strings.TrimSpace(rest[toIdx+4:])

	decodedRole, err := parseRoleIdentifier(rolePart)
	if err != nil {
		return nil, err
	}
	if decodedRole != expectedRole {
		return nil, fmt.Errorf(
			"grant role mismatch: expected %q, got %q",
			expectedRole, rolePart,
		)
	}

	rawPrivileges := strings.Split(privPart, ",")
	privileges := make([]string, 0, len(rawPrivileges))
	for _, p := range rawPrivileges {
		p = strings.TrimSpace(p)
		if !validPrivilegeName(p) {
			return nil, fmt.Errorf("empty privilege in grant: %q", statement)
		}
		privileges = append(privileges, p)
	}

	var database string
	switch {
	case scopePart == "*.*" || scopePart == "*":
		database = "*"

	case strings.HasSuffix(scopePart, ".*"):
		var err error
		database, err = parseRoleIdentifier(strings.TrimSuffix(scopePart, ".*"))
		if err != nil {
			return nil, err
		}
		if database == "" {
			return nil, fmt.Errorf("invalid database scope: %q", scopePart)
		}

	default:
		return nil, fmt.Errorf("table-level grants are not supported: %s", scopePart)
	}

	grants := make([]CHGrant, 0, len(privileges))
	for _, p := range privileges {
		grants = append(grants, CHGrant{
			RoleName:   expectedRole,
			AccessType: p,
			Database:   database,
			Scope:      scopePart,
		})
	}

	return grants, nil
}

func (rs *CHRoleService) GetRole(ctx context.Context, roleName string) (*CHRole, error) {
	rows, err := (*rs.CHConnection).Query(ctx, "SELECT name FROM system.roles WHERE name = ?", roleName)
	if err != nil {
		return nil, fmt.Errorf("fetch role: %w", err)
	}
	exists := rows.Next()
	scanErr := rows.Err()
	closeErr := rows.Close()
	if scanErr != nil {
		return nil, fmt.Errorf("read role: %w", scanErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close role query: %w", closeErr)
	}
	if !exists {
		return nil, nil
	}
	privileges, err := rs.getRoleGrants(ctx, roleName)
	if err != nil {
		return nil, err
	}
	effective, rawPrivileges, err := rs.rawRoleGrants(ctx, roleName)
	if err != nil {
		return nil, err
	}
	return &CHRole{Name: roleName, Privileges: privileges, EffectivePrivileges: effective, RawPrivileges: rawPrivileges}, nil
}

func (rs *CHRoleService) executeGrants(ctx context.Context, catalog privilegeCatalog, name, database string, privileges []string) error {
	for _, query := range grantQueries(catalog, name, privileges, database) {
		if err := (*rs.CHConnection).Exec(ctx, query); err != nil {
			return fmt.Errorf("grant privileges to role %q: %w", name, err)
		}
	}
	return nil
}

func (rs *CHRoleService) UpdateRole(ctx context.Context, plan RoleResource, data *schema.ResourceData) (*CHRole, error) {
	catalog, err := rs.privilegeCatalog(ctx)
	if err != nil {
		return nil, err
	}
	privileges := common.StringSetToList(plan.Privileges)
	if err := catalog.validateScope(plan.Database, privileges); err != nil {
		return nil, err
	}
	oldName, _ := data.GetChange("name")
	role, err := rs.GetRole(ctx, oldName.(string))
	if err != nil {
		return nil, err
	}
	if role == nil {
		return nil, fmt.Errorf("role %q not found", oldName)
	}
	oldPrivileges, _ := data.GetChange("privileges")
	previous := common.StringSetToList(oldPrivileges.(*schema.Set))
	directional := catalog.sourceDirection(previous) || catalog.sourceDirection(privileges)
	conn := *rs.CHConnection
	if data.HasChange("name") {
		if err := conn.Exec(ctx, fmt.Sprintf("ALTER ROLE %s RENAME TO %s", quoteRoleIdentifier(role.Name), quoteRoleIdentifier(plan.Name))); err != nil {
			return nil, fmt.Errorf("rename role: %w", err)
		}
	}
	replace := directional || data.HasChange("database") || catalog.replacementRequired(previous, privileges)
	if directional && (data.HasChange("database") || data.HasChange("privileges")) && len(privileges) > 0 {
		// direct replacement checks grant options atomically, while [CURRENT GRANTS] can omit ungrantable rights
		query := fmt.Sprintf("GRANT %s ON %s TO %s WITH REPLACE OPTION", strings.Join(privileges, ","), roleScope(plan.Database), quoteRoleIdentifier(plan.Name))
		if err := conn.Exec(ctx, query); err != nil {
			return nil, fmt.Errorf("replace directional source privileges: %w", err)
		}
	} else if data.HasChange("privileges") && !replace {
		previousSet := map[string]bool{}
		for _, privilege := range previous {
			previousSet[strings.ToUpper(privilege)] = true
		}
		var additions []string
		for _, privilege := range privileges {
			if !previousSet[strings.ToUpper(privilege)] {
				additions = append(additions, privilege)
			}
		}
		if err := rs.executeGrants(ctx, catalog, plan.Name, plan.Database, additions); err != nil {
			return nil, err
		}
		for _, query := range revokeQueries(catalog, plan.Name, role.Privileges, privileges, plan.Database) {
			if err := conn.Exec(ctx, query); err != nil {
				return nil, fmt.Errorf("revoke removed role privileges: %w", err)
			}
		}
	} else if data.HasChange("database") || data.HasChange("privileges") {
		if len(privileges) == 0 {
			if err := conn.Exec(ctx, "REVOKE ALL ON *.* FROM "+quoteRoleIdentifier(plan.Name)); err != nil {
				return nil, fmt.Errorf("revoke role privileges: %w", err)
			}
		} else {
			if !data.HasChange("database") && (plan.Database == "*" || plan.Database == "system") {
				wanted := map[string]bool{}
				for _, privilege := range privileges {
					catalog.leaves(privilege, plan.Database, wanted)
				}
				for _, grant := range role.Privileges {
					existing := map[string]bool{}
					catalog.leaves(grant.AccessType, grant.Database, existing)
					covered := len(existing) > 0
					for privilege := range existing {
						if !wanted[privilege] {
							covered = false
						}
					}
					if !covered {
						continue
					}
					query := fmt.Sprintf("GRANT %s ON %s TO %s", grant.AccessType, grant.Scope, quoteRoleIdentifier(plan.Name))
					if err := conn.Exec(ctx, query); err != nil {
						return nil, fmt.Errorf("cannot preserve unchanged privilege %s on %s: %w", grant.AccessType, grant.Scope, err)
					}
				}
			}
			if err := rs.executeGrants(ctx, catalog, plan.Name, plan.Database, privileges); err != nil {
				return nil, err
			}
			query := replacementGrantQuery(plan.Name, privileges, plan.Database)
			if err := conn.Exec(ctx, query); err != nil {
				return nil, fmt.Errorf("replace role privileges: %w", err)
			}
		}
	}

	return rs.GetRole(ctx, plan.Name)
}

func (rs *CHRoleService) CreateRole(ctx context.Context, name, database string, privileges []string) (*CHRole, error) {
	catalog, err := rs.privilegeCatalog(ctx)
	if err != nil {
		return nil, err
	}
	if err := catalog.validateScope(database, privileges); err != nil {
		return nil, err
	}
	conn := *rs.CHConnection
	if err := conn.Exec(ctx, "CREATE ROLE "+quoteRoleIdentifier(name)); err != nil {
		return nil, fmt.Errorf("create role: %w", err)
	}
	if err := rs.executeGrants(ctx, catalog, name, database, privileges); err != nil {
		if cleanupErr := rs.DeleteRole(ctx, name); cleanupErr != nil {
			return nil, fmt.Errorf("%w (role cleanup failed: %v)", err, cleanupErr)
		}
		return nil, err
	}
	role, err := rs.GetRole(ctx, name)
	if err != nil {
		if cleanupErr := rs.DeleteRole(ctx, name); cleanupErr != nil {
			return nil, fmt.Errorf("%w (role cleanup failed: %v)", err, cleanupErr)
		}
		return nil, err
	}
	return role, nil
}

func (rs *CHRoleService) DeleteRole(ctx context.Context, name string) error {
	if err := (*rs.CHConnection).Exec(ctx, "DROP ROLE "+quoteRoleIdentifier(name)); err != nil {
		return fmt.Errorf("drop role: %w", err)
	}
	return nil
}
