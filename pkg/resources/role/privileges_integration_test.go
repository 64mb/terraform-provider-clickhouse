package resourcerole

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRolePrivileges(t *testing.T) {
	address := os.Getenv("CLICKHOUSE_TEST_ADDR")
	if address == "" {
		t.Skip("set CLICKHOUSE_TEST_ADDR to a disposable ClickHouse native endpoint")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}, Auth: clickhouse.Auth{Username: "default"}})
	require.NoError(t, err)
	defer conn.Close()
	ctx := context.Background()
	require.NoError(t, conn.Ping(ctx))
	service := CHRoleService{CHConnection: &conn}
	client := &common.ApiClient{ClickhouseConnection: &conn}
	database := fmt.Sprintf("role_privileges_%d", time.Now().UnixNano())
	require.NoError(t, conn.Exec(ctx, "CREATE DATABASE "+quoteRoleIdentifier(database)))
	defer conn.Exec(ctx, "DROP DATABASE "+quoteRoleIdentifier(database))
	cases := []struct {
		name, database string
		privileges     []string
	}{
		{"all_database", database, []string{"ALL"}},
		{"all_global", "*", []string{"ALL"}},
		{"optimize", database, []string{"OPTIMIZE"}},
		{"alter", database, []string{"ALTER"}},
		{"sources", "*", []string{"REMOTE", "S3"}},
		{"mixed", "*", []string{"REMOTE", "SELECT"}},
		{"new_global", "*", []string{"SYSTEM DROP QUERY CACHE"}},
		{"quoted TO role", database, []string{"SELECT"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			name := database + "_" + test.name
			role, err := service.CreateRole(ctx, name, test.database, test.privileges)
			require.NoError(t, err)
			defer service.DeleteRole(ctx, name)
			data := schema.TestResourceDataRaw(t, ResourceRole().Schema, map[string]interface{}{"name": name, "database": test.database, "privileges": common.StringListToSet(test.privileges).List()})
			data.SetId(name)
			require.NoError(t, recordRoleGrants(data, role))
			require.False(t, resourceRoleRead(ctx, data, client).HasError())
			require.True(t, sameStringSet(common.StringListToSet(test.privileges), data.Get("privileges").(*schema.Set)), "actual=%v", data.Get("privileges"))
			require.Equal(t, test.database, data.Get("database"))
			if test.name == "sources" {
				require.ElementsMatch(t, test.privileges, role.GetPrivilegesList())
			}
			require.NoError(t, conn.Exec(ctx, "REVOKE ALL ON *.* FROM "+quoteRoleIdentifier(name)))
			require.False(t, resourceRoleRead(ctx, data, client).HasError())
			require.Zero(t, data.Get("privileges").(*schema.Set).Len(), "external revoke must be detected")
			require.NoError(t, conn.Exec(ctx, "GRANT INSERT ON "+roleScope(test.database)+" TO "+quoteRoleIdentifier(name)))
			require.False(t, resourceRoleRead(ctx, data, client).HasError())
			require.True(t, data.Get("privileges").(*schema.Set).Contains("INSERT"), "external grant must be detected")
		})
	}
	t.Run("current grants retain restricted scope", func(t *testing.T) {
		actor := database + "_actor"
		name := database + "_restricted"
		require.NoError(t, conn.Exec(ctx, "CREATE USER "+quoteRoleIdentifier(actor)))
		defer conn.Exec(ctx, "DROP USER "+quoteRoleIdentifier(actor))
		require.NoError(t, conn.Exec(ctx, "GRANT CREATE ROLE, DROP ROLE, SHOW ROLES ON *.* TO "+quoteRoleIdentifier(actor)))
		require.NoError(t, conn.Exec(ctx, "GRANT SELECT ON "+roleScope(database)+" TO "+quoteRoleIdentifier(actor)+" WITH GRANT OPTION"))
		restricted, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}, Auth: clickhouse.Auth{Username: actor}})
		require.NoError(t, err)
		defer restricted.Close()
		restrictedService := CHRoleService{CHConnection: &restricted}
		role, err := restrictedService.CreateRole(ctx, name, "*", []string{"SELECT"})
		require.NoError(t, err)
		defer service.DeleteRole(ctx, name)
		require.Len(t, role.Privileges, 1)
		require.Equal(t, database, role.Privileges[0].Database)
		data := schema.TestResourceDataRaw(t, ResourceRole().Schema, map[string]interface{}{"name": name, "database": "*", "privileges": []interface{}{"SELECT"}})
		data.SetId(name)
		require.NoError(t, recordRoleGrants(data, role))
		require.False(t, resourceRoleRead(ctx, data, client).HasError())
		require.Equal(t, "*", data.Get("database"))
		require.Equal(t, []interface{}{"SELECT"}, data.Get("privileges").(*schema.Set).List())
		directName := name + "_failed_update"
		directRole, err := restrictedService.CreateRole(ctx, directName, database, []string{"SELECT"})
		require.NoError(t, err)
		defer service.DeleteRole(ctx, directName)
		directData := schema.TestResourceDataRaw(t, ResourceRole().Schema, map[string]interface{}{"name": directName, "database": database, "privileges": []interface{}{"SELECT"}})
		directData.SetId(directName)
		require.NoError(t, recordRoleGrants(directData, directRole))
		for _, privilege := range []string{"OPTIMIZE", "FUTURE VALID PRIVILEGE"} {
			config := terraform.NewResourceConfigRaw(map[string]interface{}{"name": directName, "database": database, "privileges": []interface{}{"SELECT", privilege}})
			diff, err := ResourceRole().Diff(ctx, directData.State(), config, &common.ApiClient{ClickhouseConnection: &restricted})
			require.NoError(t, err)
			_, diagnostics := ResourceRole().Apply(ctx, directData.State(), diff, &common.ApiClient{ClickhouseConnection: &restricted})
			require.True(t, diagnostics.HasError())
			unchanged, err := service.GetRole(ctx, directName)
			require.NoError(t, err)
			require.Equal(t, []string{"SELECT"}, unchanged.GetPrivilegesList(), "failed additions preserve existing rights")
		}

		require.NoError(t, conn.Exec(ctx, "REVOKE GRANT OPTION FOR SELECT ON "+roleScope(database)+" FROM "+quoteRoleIdentifier(actor)))

		require.NoError(t, conn.Exec(ctx, "GRANT INSERT ON "+roleScope(database)+" TO "+quoteRoleIdentifier(actor)+" WITH GRANT OPTION"))
		incrementalName := name + "_incremental"
		incrementalRole, err := service.CreateRole(ctx, incrementalName, database, []string{"SELECT"})
		require.NoError(t, err)
		defer service.DeleteRole(ctx, incrementalName)
		incrementalData := schema.TestResourceDataRaw(t, ResourceRole().Schema, map[string]interface{}{"name": incrementalName, "database": database, "privileges": []interface{}{"SELECT"}})
		incrementalData.SetId(incrementalName)
		require.NoError(t, recordRoleGrants(incrementalData, incrementalRole))
		for _, desired := range [][]interface{}{{"SELECT", "INSERT"}, {"SELECT"}} {
			config := terraform.NewResourceConfigRaw(map[string]interface{}{"name": incrementalName, "database": database, "privileges": desired})
			diff, err := ResourceRole().Diff(ctx, incrementalData.State(), config, &common.ApiClient{ClickhouseConnection: &restricted})
			require.NoError(t, err)
			state, diagnostics := ResourceRole().Apply(ctx, incrementalData.State(), diff, &common.ApiClient{ClickhouseConnection: &restricted})
			require.False(t, diagnostics.HasError(), "unchanged SELECT does not require grant option: %v", diagnostics)
			actual, err := service.GetRole(ctx, incrementalName)
			require.NoError(t, err)
			require.ElementsMatch(t, desired, common.StringListToSet(actual.GetPrivilegesList()).List())
			incrementalData = ResourceRole().Data(state)
		}

		sourceName := name + "_source_incremental"
		sourceRole, err := service.CreateRole(ctx, sourceName, "*", []string{"S3", "SELECT"})
		require.NoError(t, err)
		defer service.DeleteRole(ctx, sourceName)
		sourceData := schema.TestResourceDataRaw(t, ResourceRole().Schema, map[string]interface{}{"name": sourceName, "database": "*", "privileges": []interface{}{"S3", "SELECT"}})
		sourceData.SetId(sourceName)
		require.NoError(t, recordRoleGrants(sourceData, sourceRole))
		for _, desired := range [][]interface{}{{"S3", "SELECT", "INSERT"}, {"S3", "SELECT"}} {
			config := terraform.NewResourceConfigRaw(map[string]interface{}{"name": sourceName, "database": "*", "privileges": desired})
			diff, err := ResourceRole().Diff(ctx, sourceData.State(), config, &common.ApiClient{ClickhouseConnection: &restricted})
			require.NoError(t, err)
			state, diagnostics := ResourceRole().Apply(ctx, sourceData.State(), diff, &common.ApiClient{ClickhouseConnection: &restricted})
			require.False(t, diagnostics.HasError(), "unchanged S3 and SELECT do not require grant option: %v", diagnostics)
			actual, err := service.GetRole(ctx, sourceName)
			require.NoError(t, err)
			require.ElementsMatch(t, desired, common.StringListToSet(actual.GetPrivilegesList()).List())
			sourceData = ResourceRole().Data(state)
		}
		globalConfig := terraform.NewResourceConfigRaw(map[string]interface{}{"name": name, "database": "*", "privileges": []interface{}{"ALL"}})
		globalDiff, err := ResourceRole().Diff(ctx, data.State(), globalConfig, &common.ApiClient{ClickhouseConnection: &restricted})
		require.NoError(t, err)
		_, diagnostics := ResourceRole().Apply(ctx, data.State(), globalDiff, &common.ApiClient{ClickhouseConnection: &restricted})
		require.True(t, diagnostics.HasError(), "do not drop unchanged rights after the actor loses grant option")
		unchanged, err := service.GetRole(ctx, name)
		require.NoError(t, err)
		require.Equal(t, []string{"SELECT"}, unchanged.GetPrivilegesList())

	})

	t.Run("quoted database and role", func(t *testing.T) {
		quotedDatabase := database + " TO db`\\name"
		quotedRole := database + " ON role`\\name"
		require.NoError(t, conn.Exec(ctx, "CREATE DATABASE "+quoteRoleIdentifier(quotedDatabase)))
		defer conn.Exec(ctx, "DROP DATABASE "+quoteRoleIdentifier(quotedDatabase))
		role, err := service.CreateRole(ctx, quotedRole, quotedDatabase, []string{"OPTIMIZE"})
		require.NoError(t, err)
		defer service.DeleteRole(ctx, quotedRole)
		require.Equal(t, quotedDatabase, role.Privileges[0].Database)
		require.Equal(t, quotedRole, role.Name)
	})

	t.Run("source write drift with unchanged show grants", func(t *testing.T) {
		catalog, err := service.privilegeCatalog(ctx)
		require.NoError(t, err)
		if _, ok := catalog["READ"]; !ok {
			t.Skip("server predates separate source READ/WRITE privileges")
		}
		name := database + "_source_drift"
		role, err := service.CreateRole(ctx, name, "*", []string{"S3"})
		require.NoError(t, err)
		defer service.DeleteRole(ctx, name)
		data := schema.TestResourceDataRaw(t, ResourceRole().Schema, map[string]interface{}{"name": name, "database": "*", "privileges": []interface{}{"S3"}})
		data.SetId(name)
		require.NoError(t, recordRoleGrants(data, role))
		require.NoError(t, conn.Exec(ctx, "REVOKE WRITE ON S3 FROM "+quoteRoleIdentifier(name)))
		actual, err := service.GetRole(ctx, name)
		require.NoError(t, err)
		require.ElementsMatch(t, role.GetPrivilegesList(), actual.GetPrivilegesList(), "SHOW GRANTS hides source read/write difference")
		require.False(t, resourceRoleRead(ctx, data, client).HasError())
		require.True(t, data.Get("privileges").(*schema.Set).Contains("READ"))
		require.False(t, data.Get("privileges").(*schema.Set).Contains("S3"))
	})

	t.Run("global scope rejected before role creation", func(t *testing.T) {
		name := database + "_invalid"
		_, err := service.CreateRole(ctx, name, database, []string{"SYSTEM DROP QUERY CACHE"})
		require.ErrorContains(t, err, "only allowed")
		role, err := service.GetRole(ctx, name)
		require.NoError(t, err)
		require.Nil(t, role)
	})
	t.Run("unknown privilege rejected before creation", func(t *testing.T) {
		name := database + "_unknown"
		_, err := service.CreateRole(ctx, name, "*", []string{"FUTURE VALID PRIVILEGE"})
		require.ErrorContains(t, err, "not supported")
		role, err := service.GetRole(ctx, name)
		require.NoError(t, err)
		require.Nil(t, role)
	})
}

func TestIntegrationRolePrivilegesLegacyState(t *testing.T) {
	address := os.Getenv("CLICKHOUSE_TEST_ADDR")
	if address == "" {
		t.Skip("set CLICKHOUSE_TEST_ADDR to a disposable ClickHouse native endpoint")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}, Auth: clickhouse.Auth{Username: "default"}})
	require.NoError(t, err)
	defer conn.Close()
	ctx := context.Background()
	service := CHRoleService{CHConnection: &conn}
	client := &common.ApiClient{ClickhouseConnection: &conn}
	database := fmt.Sprintf("role_legacy_%d", time.Now().UnixNano())
	require.NoError(t, conn.Exec(ctx, "CREATE DATABASE "+quoteRoleIdentifier(database)))
	defer conn.Exec(ctx, "DROP DATABASE "+quoteRoleIdentifier(database))
	for _, test := range []struct {
		name, database string
		privileges     []interface{}
	}{
		{"select", database, []interface{}{"SELECT"}},
		{"empty", database, []interface{}{}},
		{"alter", database, []interface{}{"ALTER"}},
		{"sources", "*", []interface{}{"REMOTE", "S3"}},
		{"system", "system", []interface{}{"SELECT"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := database + "_" + test.name
			var privileges []string
			for _, value := range test.privileges {
				privileges = append(privileges, value.(string))
			}
			_, err := service.CreateRole(ctx, name, test.database, privileges)
			require.NoError(t, err)
			defer conn.Exec(ctx, "DROP ROLE IF EXISTS "+quoteRoleIdentifier(name))
			defer conn.Exec(ctx, "DROP ROLE IF EXISTS "+quoteRoleIdentifier(name+"_renamed"))
			config := map[string]interface{}{"name": name, "database": test.database, "privileges": test.privileges}
			data := schema.TestResourceDataRaw(t, ResourceRole().Schema, config)
			data.SetId(name)
			require.Zero(t, data.Get("granted_privileges").(*schema.Set).Len())
			require.False(t, resourceRoleRead(ctx, data, client).HasError())
			require.Equal(t, test.database, data.Get("database"))
			require.True(t, sameStringSet(common.StringListToSet(privileges), data.Get("privileges").(*schema.Set)))
			diff, err := ResourceRole().Diff(ctx, data.State(), terraform.NewResourceConfigRaw(config), client)
			require.NoError(t, err)
			require.True(t, diff.Empty(), "legacy refresh must leave an unchanged plan")
			config["name"] = name + "_renamed"
			if test.database != "*" {
				config["database"] = "default"
			}
			diff, err = ResourceRole().Diff(ctx, data.State(), terraform.NewResourceConfigRaw(config), client)
			require.NoError(t, err)
			state, diagnostics := ResourceRole().Apply(ctx, data.State(), diff, client)
			require.False(t, diagnostics.HasError(), "%v", diagnostics)
			refreshed := ResourceRole().Data(state)
			require.False(t, resourceRoleRead(ctx, refreshed, client).HasError())
			diff, err = ResourceRole().Diff(ctx, refreshed.State(), terraform.NewResourceConfigRaw(config), client)
			require.NoError(t, err)
			require.True(t, diff.Empty(), "migrated name and database must leave an unchanged plan")
		})
	}
}
