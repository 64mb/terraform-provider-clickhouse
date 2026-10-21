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
)

func TestIntegrationRoleReadSmallPool(t *testing.T) {
	addr := os.Getenv("CLICKHOUSE_TEST_ADDR")
	if addr == "" {
		t.Skip("CLICKHOUSE_TEST_ADDR is not set")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Username: "default"}, MaxOpenConns: 1, MaxIdleConns: 1, DialTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	name := fmt.Sprintf("terraform_cleanup_role_%d", time.Now().UnixNano())
	if err := conn.Exec(ctx, "CREATE ROLE "+name); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := conn.Exec(cleanupCtx, "DROP ROLE IF EXISTS "+name); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()
	service := CHRoleService{CHConnection: &conn}
	if err := conn.Exec(ctx, "GRANT SELECT ON default.* TO "+name); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		role, err := service.GetRole(ctx, name)
		if err != nil || role == nil || len(role.Privileges) != 1 || role.Privileges[0].AccessType != "SELECT" {
			t.Fatalf("read %d: role=%#v, error=%v", i, role, err)
		}
	}
	if err := conn.Exec(ctx, "REVOKE SELECT ON default.* FROM "+name); err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(ctx, "GRANT SELECT ON default.cleanup_table TO "+name); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		if _, err := service.GetRole(ctx, name); err == nil {
			t.Fatalf("table grant read %d should fail", i)
		}
	}
	if err := conn.Exec(ctx, "REVOKE SELECT ON default.cleanup_table FROM "+name); err != nil {
		t.Fatal(err)
	}
	if role, err := service.GetRole(ctx, name); err != nil || role == nil || len(role.Privileges) != 0 {
		t.Fatalf("read after errors: role=%#v, error=%v", role, err)
	}
	data := schema.TestResourceDataRaw(t, ResourceRole().Schema, map[string]interface{}{"name": name, "database": "default"})
	data.SetId(name)
	if err := conn.Exec(ctx, "DROP ROLE "+name); err != nil {
		t.Fatal(err)
	}
	if diags := resourceRoleRead(ctx, data, &common.ApiClient{ClickhouseConnection: &conn}); diags.HasError() || data.Id() != "" {
		t.Fatalf("missing refresh: ID=%q, diagnostics=%v", data.Id(), diags)
	}

}
