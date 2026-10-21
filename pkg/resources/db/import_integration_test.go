package resourcedb

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

func TestIntegrationDatabaseMissingRefresh(t *testing.T) {
	addr := os.Getenv("CLICKHOUSE_TEST_ADDR")
	if addr == "" {
		t.Skip("set CLICKHOUSE_TEST_ADDR to run database refresh tests")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Username: "default"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	r := ResourceDb()
	d := schema.TestResourceDataRaw(t, r.Schema, map[string]interface{}{"name": fmt.Sprintf("terraform_missing_%d", time.Now().UnixNano())})
	d.SetId(":" + d.Get("name").(string))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if diags := r.ReadContext(ctx, d, &common.ApiClient{ClickhouseConnection: &conn}); diags.HasError() {
		t.Fatalf("missing refresh: %v", diags)
	}
	if d.Id() != "" {
		t.Fatalf("missing database retained ID %q", d.Id())
	}
}
