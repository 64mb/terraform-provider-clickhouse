package resourceuser

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

func TestIntegrationUserReadSmallPool(t *testing.T) {
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
	name := fmt.Sprintf("terraform_cleanup_user_%d", time.Now().UnixNano())
	if err := conn.Exec(ctx, "CREATE USER "+name); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		if err := conn.Exec(cleanupCtx, "DROP USER IF EXISTS "+name); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	}()
	service := CHUserService{CHConnection: &conn}
	for i := 0; i < 20; i++ {
		user, err := service.GetUser(ctx, name)
		if err != nil || user == nil || user.Name != name {
			t.Fatalf("read %d: user=%#v, error=%v", i, user, err)
		}
		missing, err := service.GetUser(ctx, name+"_missing")
		if err != nil || missing != nil {
			t.Fatalf("missing read %d: user=%#v, error=%v", i, missing, err)
		}
		canceled, stop := context.WithCancel(ctx)
		stop()
		if _, err := service.GetUser(canceled, name); err == nil {
			t.Fatalf("canceled read %d succeeded", i)
		}
	}
	data := schema.TestResourceDataRaw(t, ResourceUser().Schema, map[string]interface{}{"name": name, "password": "secret"})
	data.SetId(name)
	if err := conn.Exec(ctx, "DROP USER "+name); err != nil {
		t.Fatal(err)
	}
	if diags := resourceUserRead(ctx, data, &common.ApiClient{ClickhouseConnection: &conn}); diags.HasError() || data.Id() != "" {
		t.Fatalf("missing refresh: ID=%q, diagnostics=%v", data.Id(), diags)
	}

}
