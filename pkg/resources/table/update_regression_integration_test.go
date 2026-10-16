package resourcetable

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

func TestIntegrationImportExplicitClusterWins(t *testing.T) {
	addr := os.Getenv("CLICKHOUSE_TEST_ADDR")
	if addr == "" {
		t.Skip("CLICKHOUSE_TEST_ADDR is not set")
	}
	ctx := context.Background()
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Username: "default"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	table := TableResource{Database: "default", Name: fmt.Sprintf("tf_import_cluster_%d", time.Now().UnixNano()), Engine: "MergeTree", OrderBy: []string{"id"}, Comment: storedComment("legacy", "stale_cluster"), Columns: []interface{}{map[string]interface{}{"name": "id", "type": "UInt64"}}}
	service := CHTableService{CHConnection: &conn}
	if err := service.CreateTable(ctx, table); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(ctx, "DROP TABLE "+qualified(table))
	d := schema.TestResourceDataRaw(t, ResourceTable().Schema, nil)
	d.SetId("explicit_cluster:default:" + table.Name)
	if _, err := importTable(ctx, d, nil); err != nil {
		t.Fatal(err)
	}
	if diags := resourceTableRead(ctx, d, &common.ApiClient{ClickhouseConnection: &conn}); diags.HasError() {
		t.Fatal(diags)
	}
	if d.Get("cluster") != "explicit_cluster" || d.Id() != "explicit_cluster:default:"+table.Name {
		t.Fatalf("explicit target lost: %s %v", d.Id(), d.Get("cluster"))
	}
}

func TestIntegrationEquivalentExpressionsUpdateAndRebuild(t *testing.T) {
	addr := os.Getenv("CLICKHOUSE_TEST_ADDR")
	if addr == "" {
		t.Skip("CLICKHOUSE_TEST_ADDR is not set")
	}
	ctx := context.Background()
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Username: "default"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	client := &common.ApiClient{ClickhouseConnection: &conn}
	service := CHTableService{CHConnection: &conn}
	name := fmt.Sprintf("tf_expr_%d", time.Now().UnixNano())
	d := schema.TestResourceDataRaw(t, ResourceTable().Schema, map[string]interface{}{"database": "default", "name": name, "engine": "MergeTree", "order_by": []interface{}{"toStartOfInterval(at, INTERVAL 1 HOUR)", "id"}, "partition_by": []interface{}{map[string]interface{}{"by": "toStartOfInterval(at, INTERVAL 1 DAY)"}}, "column": []interface{}{map[string]interface{}{"name": "id", "type": "UInt64"}, map[string]interface{}{"name": "at", "type": "DateTime"}}})
	table := tableFromData(d)
	defer conn.Exec(ctx, "DROP TABLE IF EXISTS "+qualified(table))
	if diags := resourceTableCreate(ctx, d, client); diags.HasError() {
		t.Fatal(diags)
	}
	uuid := d.Get("uuid")
	d = ResourceTable().Data(d.State())
	if err := conn.Exec(ctx, "INSERT INTO "+qualified(table)+" VALUES (1,now()),(2,now())"); err != nil {
		t.Fatal(err)
	}
	if err := d.Set("comment", "updated"); err != nil {
		t.Fatal(err)
	}
	if diags := resourceTableUpdate(ctx, d, client); diags.HasError() {
		t.Fatalf("equivalent expression triggered immutable change: %v", diags)
	}
	if d.Get("uuid") != uuid {
		t.Fatal("comment update replaced the table")
	}
	live, err := service.GetTable(ctx, "default", name)
	if err != nil {
		t.Fatal(err)
	}
	next := tableFromData(d)
	next.OrderBy = []string{"id", "toStartOfInterval(at, INTERVAL 1 HOUR)"}
	next.PrimaryKey = nil
	cfg := map[string]interface{}{"enabled": true, "maintenance_acknowledged": true, "operation_id": "expression-rebuild"}
	stage := next
	stage.Name = rebuildName(next, "expression-rebuild")
	defer conn.Exec(ctx, "DROP TABLE IF EXISTS "+qualified(stage))
	backup, err := service.rebuildTable(ctx, live, next, cfg, false)
	if err != nil {
		t.Fatalf("equivalent target expressions rejected: %v", err)
	}
	for _, n := range []string{name, backup} {
		var count uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM default."+quoteIdent(n)).Scan(&count); err != nil || count != 2 {
			t.Fatalf("retained rows %s: %d %v", n, count, err)
		}
	}
}
