package resourcerole

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"github.com/stretchr/testify/require"
)

func TestIntegrationRoleSourceUpdates(t *testing.T) {
	address := os.Getenv("CLICKHOUSE_TEST_ADDR")
	if address == "" {
		t.Skip("set CLICKHOUSE_TEST_ADDR to a disposable ClickHouse native endpoint")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}, Auth: clickhouse.Auth{Username: "default"}, MaxOpenConns: 1})
	require.NoError(t, err)
	defer conn.Close()
	ctx := context.Background()
	service := CHRoleService{CHConnection: &conn}
	catalog, err := service.privilegeCatalog(ctx)
	require.NoError(t, err)
	if _, ok := catalog["READ"]; !ok {
		t.Skip("server privilege catalog does not support READ")
	}
	client := &common.ApiClient{ClickhouseConnection: &conn}
	cases := []struct {
		name  string
		steps [][]string
	}{
		{"read", [][]string{{"READ"}, {"READ", "SELECT"}, {"READ"}, {"WRITE"}, {"READ", "WRITE"}, {"S3"}, {"SELECT"}, {"READ"}, {}}},
		{"write", [][]string{{"WRITE"}, {"WRITE", "SELECT"}, {"WRITE"}}},
		{"mixed", [][]string{{"READ", "S3"}, {"READ", "S3", "SELECT"}, {"READ", "S3"}, {"READ"}, {"READ", "S3"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name := fmt.Sprintf("audit_sourcefix_%d_%s", time.Now().UnixNano(), tc.name)
			defer conn.Exec(ctx, "DROP ROLE IF EXISTS "+quoteRoleIdentifier(name))
			resource := ResourceRole()
			var state *terraform.InstanceState
			for _, privileges := range tc.steps {
				config := terraform.NewResourceConfigRaw(map[string]interface{}{"name": name, "database": "*", "privileges": common.StringListToSet(privileges).List()})
				diff, err := resource.Diff(ctx, state, config, client)
				require.NoError(t, err)
				next, diagnostics := resource.Apply(ctx, state, diff, client)
				require.False(t, diagnostics.HasError(), "%v", diagnostics)
				data := resource.Data(next)
				require.False(t, resource.ReadContext(ctx, data, client).HasError())
				state = data.State()
				diff, err = resource.Diff(ctx, state, config, client)
				require.NoError(t, err)
				require.True(t, diff.Empty(), "privileges=%v diff=%v", privileges, diff)
				expectedName := name + "_expected"
				expected, err := service.CreateRole(ctx, expectedName, "*", privileges)
				require.NoError(t, err)
				actual, err := service.GetRole(ctx, name)
				require.NoError(t, err)
				require.NoError(t, service.DeleteRole(ctx, expectedName))
				require.ElementsMatch(t, expected.EffectivePrivileges, actual.EffectivePrivileges, "privileges=%v", privileges)
			}
		})
	}
}

func TestIntegrationRoleSourceUpdateDeniedPreservesGrants(t *testing.T) {
	address := os.Getenv("CLICKHOUSE_TEST_ADDR")
	if address == "" {
		t.Skip("set CLICKHOUSE_TEST_ADDR to a disposable ClickHouse native endpoint")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}, Auth: clickhouse.Auth{Username: "default"}, MaxOpenConns: 1})
	require.NoError(t, err)
	defer conn.Close()
	ctx := context.Background()
	service := CHRoleService{CHConnection: &conn}
	catalog, err := service.privilegeCatalog(ctx)
	require.NoError(t, err)
	if _, ok := catalog["READ"]; !ok {
		t.Skip("server privilege catalog does not support READ")
	}
	name := fmt.Sprintf("audit_sourcefix_denied_%d", time.Now().UnixNano())
	actor := name + "_actor"
	require.NoError(t, conn.Exec(ctx, "CREATE USER "+quoteRoleIdentifier(actor)))
	defer conn.Exec(ctx, "DROP USER "+quoteRoleIdentifier(actor))
	require.NoError(t, conn.Exec(ctx, "GRANT CREATE ROLE, DROP ROLE, SHOW ROLES ON *.* TO "+quoteRoleIdentifier(actor)))
	require.NoError(t, conn.Exec(ctx, "GRANT SELECT ON *.* TO "+quoteRoleIdentifier(actor)+" WITH GRANT OPTION"))
	require.NoError(t, conn.Exec(ctx, "GRANT READ ON *.* TO "+quoteRoleIdentifier(actor)))
	before, err := service.CreateRole(ctx, name, "*", []string{"READ"})
	require.NoError(t, err)
	defer service.DeleteRole(ctx, name)
	restricted, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}, Auth: clickhouse.Auth{Username: actor}, MaxOpenConns: 1})
	require.NoError(t, err)
	defer restricted.Close()
	resource := ResourceRole()
	data := resource.Data(nil)
	require.NoError(t, data.Set("name", name))
	require.NoError(t, data.Set("database", "*"))
	require.NoError(t, data.Set("privileges", []interface{}{"READ"}))
	data.SetId(name)
	require.NoError(t, recordRoleGrants(data, before))
	config := terraform.NewResourceConfigRaw(map[string]interface{}{"name": name, "database": "*", "privileges": []interface{}{"READ", "SELECT"}})
	client := &common.ApiClient{ClickhouseConnection: &restricted}
	diff, err := resource.Diff(ctx, data.State(), config, client)
	require.NoError(t, err)
	_, diagnostics := resource.Apply(ctx, data.State(), diff, client)
	require.True(t, diagnostics.HasError(), "missing unchanged READ grant option must reject replacement")
	after, err := service.GetRole(ctx, name)
	require.NoError(t, err)
	require.ElementsMatch(t, before.EffectivePrivileges, after.EffectivePrivileges)
}
