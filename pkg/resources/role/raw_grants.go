package resourcerole

import (
	"context"
	"errors"
	"fmt"
	"github.com/ClickHouse/clickhouse-go/v2"
)

func (rs *CHRoleService) rawRoleGrants(ctx context.Context, name string) (result []string, privileges []string, err error) {
	conn := *rs.CHConnection
	columns, err := conn.Query(ctx, "SELECT name FROM system.columns WHERE database = 'system' AND table = 'grants' AND name IN ('access_object', 'is_wildcard')")
	if err != nil {
		if roleGrantMetadataDenied(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("inspect grant metadata columns: %w", err)
	}
	object, wildcard := "''", "toUInt8(0)"
	for columns.Next() {
		var column string
		if err := columns.Scan(&column); err != nil {
			columns.Close()
			return nil, nil, err
		}
		if column == "access_object" {
			object = "access_object"
		}
		if column == "is_wildcard" {
			wildcard = "is_wildcard"
		}
	}
	if err := columns.Err(); err != nil {
		columns.Close()
		return nil, nil, err
	}
	if err := columns.Close(); err != nil {
		return nil, nil, err
	}
	query := "SELECT toString(access_type), database, table, column, grant_option, " + object + ", " + wildcard + " FROM system.grants WHERE role_name = ? AND is_partial_revoke = 0"
	rows, err := conn.Query(ctx, query, name)
	if err != nil {
		if roleGrantMetadataDenied(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read effective role privileges: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			result = nil
			privileges = nil
			err = fmt.Errorf("close effective role privileges: %w", closeErr)
		}
	}()
	result = []string{}
	for rows.Next() {
		var privilege, accessObject string
		var database, table, column *string
		var grantOption, isWildcard uint8
		if err := rows.Scan(&privilege, &database, &table, &column, &grantOption, &accessObject, &isWildcard); err != nil {
			return nil, nil, fmt.Errorf("scan effective role privilege: %w", err)
		}
		scope := "*.*"
		if database != nil {
			scope = quoteRoleIdentifier(*database) + ".*"
			if table != nil {
				scope = quoteRoleIdentifier(*database) + "." + quoteRoleIdentifier(*table)
			}
		}
		entry := privilege + " ON " + scope
		if accessObject != "" {
			entry += " OBJECT " + quoteRoleIdentifier(accessObject)
		}
		if column != nil {
			entry += " COLUMN " + quoteRoleIdentifier(*column)
		}
		if grantOption != 0 {
			entry += " WITH GRANT OPTION"
		}
		if isWildcard != 0 {
			entry += " WITH WILDCARD"
		}
		result = append(result, entry)
		privileges = append(privileges, privilege)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("read effective role privileges: %w", err)
	}
	return result, privileges, nil
}

func roleGrantMetadataDenied(err error) bool {
	var exception *clickhouse.Exception
	return errors.As(err, &exception) && exception.Code == 497
}
