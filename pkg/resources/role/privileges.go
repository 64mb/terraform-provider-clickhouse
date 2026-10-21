package resourcerole

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type privilegeDefinition struct {
	Name    string
	Level   string
	Parent  string
	Aliases []string
}

type privilegeCatalog map[string]privilegeDefinition

func (rs *CHRoleService) privilegeCatalog(ctx context.Context) (catalog privilegeCatalog, err error) {
	rows, err := (*rs.CHConnection).Query(ctx, "SELECT toString(privilege), ifNull(toString(level), ''), ifNull(toString(parent_group), ''), aliases FROM system.privileges")
	if err != nil {
		return nil, fmt.Errorf("read server privilege definitions: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			catalog = nil
			err = fmt.Errorf("close server privilege definitions: %w", closeErr)
		}
	}()
	catalog = privilegeCatalog{}
	for rows.Next() {
		var definition privilegeDefinition
		if err := rows.Scan(&definition.Name, &definition.Level, &definition.Parent, &definition.Aliases); err != nil {
			return nil, fmt.Errorf("scan privilege definition: %w", err)
		}
		catalog[strings.ToUpper(definition.Name)] = definition
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read privilege definitions: %w", err)
	}
	for _, definition := range catalog {
		for _, alias := range definition.Aliases {
			catalog[strings.ToUpper(alias)] = definition
		}
	}
	return catalog, nil
}

func (catalog privilegeCatalog) global(name string) bool {
	definition, ok := catalog[strings.ToUpper(name)]
	if !ok {
		return false
	}
	if definition.Level != "" {
		return definition.Level == "GLOBAL"
	}
	found := false
	for _, child := range catalog {
		if child.Parent == definition.Name {
			found = true
			if !catalog.global(child.Name) {
				return false
			}
		}
	}
	return found
}

func (catalog privilegeCatalog) validateScope(database string, privileges []string) error {
	for _, privilege := range privileges {
		if !validPrivilegeName(privilege) {
			return fmt.Errorf("invalid privilege name %q", privilege)
		}
		if _, ok := catalog[strings.ToUpper(privilege)]; !ok {
			return fmt.Errorf("privilege %q is not supported by this ClickHouse server", privilege)
		}
		if database != "*" && catalog.globalOnly(privilege) {
			return fmt.Errorf("Global privilege %s is only allowed for database '*'", privilege)
		}
	}
	return nil
}

func quoteRoleIdentifier(name string) string {
	return "`" + strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(name) + "`"
}

func roleScope(database string) string {
	if database == "*" {
		return "*.*"
	}
	return quoteRoleIdentifier(database) + ".*"
}

func grantQueries(catalog privilegeCatalog, role string, privileges []string, database string) []string {
	var direct, current []string
	for _, privilege := range privileges {
		if (database == "*" || database == "system") && !catalog.global(privilege) {
			current = append(current, privilege)
		} else {
			direct = append(direct, privilege)
		}
	}
	var queries []string
	if len(direct) > 0 {
		queries = append(queries, fmt.Sprintf("GRANT %s ON %s TO %s", strings.Join(direct, ","), roleScope(database), quoteRoleIdentifier(role)))
	}
	if len(current) > 0 {
		queries = append(queries, fmt.Sprintf("GRANT CURRENT GRANTS (%s ON %s) TO %s", strings.Join(current, ","), roleScope(database), quoteRoleIdentifier(role)))
	}
	return queries
}

func replacementGrantQuery(role string, privileges []string, database string) string {
	if database == "*" || database == "system" {
		return fmt.Sprintf("GRANT CURRENT GRANTS (%s ON %s) TO %s WITH REPLACE OPTION", strings.Join(privileges, ","), roleScope(database), quoteRoleIdentifier(role))
	}
	return fmt.Sprintf("GRANT %s ON %s TO %s WITH REPLACE OPTION", strings.Join(privileges, ","), roleScope(database), quoteRoleIdentifier(role))
}

func (catalog privilegeCatalog) globalOnly(name string) bool {
	definition, ok := catalog[strings.ToUpper(name)]
	if !ok {
		return false
	}
	if definition.Level != "" {
		switch definition.Level {
		case "DATABASE", "TABLE", "COLUMN", "VIEW", "DICTIONARY":
			return false
		default:
			return true
		}
	}
	found := false
	for key, child := range catalog {
		if key == strings.ToUpper(child.Name) && child.Parent == definition.Name {
			found = true
			if !catalog.globalOnly(child.Name) {
				return false
			}
		}
	}
	return found
}

func (catalog privilegeCatalog) sourceDirection(privileges []string) bool {
	for _, privilege := range privileges {
		if catalog[strings.ToUpper(privilege)].Level == "SOURCE" {
			return true
		}
	}
	return false
}

func (catalog privilegeCatalog) replacementRequired(previous, desired []string) bool {
	aggregate := func(privileges []string) map[string]bool {
		result := map[string]bool{}
		for _, privilege := range privileges {
			definition := catalog[strings.ToUpper(privilege)]
			if definition.Name == "ALL" || definition.Name == "SOURCES" || definition.Level == "SOURCE" {
				result[definition.Name] = true
			}
		}
		return result
	}
	before, after := aggregate(previous), aggregate(desired)
	if len(before) != len(after) {
		return true
	}
	for name := range before {
		if !after[name] {
			return true
		}
	}
	return false
}

func revokeQueries(catalog privilegeCatalog, role string, previous []CHGrant, desired []string, database string) []string {
	wanted := map[string]bool{}
	for _, privilege := range desired {
		catalog.leaves(privilege, database, wanted)
	}
	byScope := map[string]map[string]bool{}
	for _, grant := range previous {
		leaves := map[string]bool{}
		catalog.leaves(grant.AccessType, grant.Database, leaves)
		for privilege := range leaves {
			if wanted[privilege] {
				continue
			}
			if byScope[grant.Scope] == nil {
				byScope[grant.Scope] = map[string]bool{}
			}
			byScope[grant.Scope][privilege] = true
		}
	}
	var scopes []string
	for scope := range byScope {
		scopes = append(scopes, scope)
	}
	sort.Strings(scopes)
	var queries []string
	for _, scope := range scopes {
		var privileges []string
		for privilege := range byScope[scope] {
			privileges = append(privileges, privilege)
		}
		sort.Strings(privileges)
		queries = append(queries, fmt.Sprintf("REVOKE %s ON %s FROM %s", strings.Join(privileges, ","), scope, quoteRoleIdentifier(role)))
	}
	return queries
}
