package resourcetable

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestIntegrationTopologyImports(t *testing.T) {
	addr := os.Getenv("CLICKHOUSE_TOPOLOGY_TEST_ADDR")
	if addr == "" {
		t.Skip("CLICKHOUSE_TOPOLOGY_TEST_ADDR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Username: "default"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	client := &common.ApiClient{ClickhouseConnection: &conn}
	service := CHTableService{CHConnection: &conn}
	for _, engine := range []string{"ReplicatedMergeTree", "ReplicatedReplacingMergeTree", "Distributed"} {
		t.Run(engine, func(t *testing.T) {
			name := fmt.Sprintf("tf_topology_%d", time.Now().UnixNano())
			localName := name + "_local"
			params := []string{quoteString("/clickhouse/tables/{shard}/" + name), "'{replica}'"}
			if engine == "ReplicatedReplacingMergeTree" {
				params = append(params, "version")
			}
			if engine == "Distributed" {
				if err := conn.Exec(ctx, "CREATE TABLE default."+quoteIdent(localName)+" (id UInt64, version UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := conn.Exec(context.Background(), "DROP TABLE IF EXISTS default."+quoteIdent(localName)); err != nil {
						t.Error(err)
					}
				}()
				params = []string{"'local_provider'", "'default'", quoteString(localName), "rand()"}
			}
			resource := TableResource{Database: "default", Name: name, Cluster: "local_provider", Engine: engine, EngineParams: params, Comment: "external topology table", Columns: []interface{}{map[string]interface{}{"name": "id", "type": "UInt64"}, map[string]interface{}{"name": "version", "type": "UInt64"}}}
			if engine != "Distributed" {
				resource.OrderBy = []string{"id"}
			}
			if err := service.CreateTable(ctx, resource); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := conn.Exec(context.Background(), "DROP TABLE IF EXISTS "+qualified(resource)+" ON CLUSTER "+quoteIdent(resource.Cluster)+" SYNC"); err != nil {
					t.Error(err)
				}
			}()
			if err := conn.Exec(ctx, "INSERT INTO "+qualified(resource)+" SETTINGS insert_distributed_sync=1 VALUES (1, 1)"); err != nil {
				t.Fatal(err)
			}
			d := schema.TestResourceDataRaw(t, ResourceTable().Schema, nil)
			d.SetId("local_provider:default:" + name)
			if _, err := importTable(ctx, d, client); err != nil {
				t.Fatal(err)
			}
			if diags := resourceTableRead(ctx, d, client); diags.HasError() {
				t.Fatalf("import refresh: %v", diags)
			}
			imported := tableFromData(d)
			if imported.Cluster != "local_provider" || imported.Engine != engine {
				t.Fatalf("import = %#v", imported)
			}
			if !reflect.DeepEqual(imported.EngineParams, params) {
				t.Fatalf("engine params = %#v, want %#v", imported.EngineParams, params)
			}
			if imported.Comment != resource.Comment {
				t.Fatalf("comment = %q", imported.Comment)
			}
			if actions, err := buildAlterPlan(imported, imported, false); err != nil || len(actions) != 0 {
				t.Fatalf("import no-op plan = %#v, %v", actions, err)
			}
			initialUUID := d.Get("uuid").(string)
			if engine == "Distributed" {
				changed := imported
				changed.Comment = "mutation"
				if _, err := buildAlterPlan(imported, changed, false); err == nil || !strings.Contains(err.Error(), "topology migration") {
					t.Fatalf("Distributed mutation error = %v", err)
				}
			} else {
				if err := d.Set("column", append(imported.Columns, map[string]interface{}{"name": "extra", "type": "Nullable(String)"})); err != nil {
					t.Fatal(err)
				}
				if diags := resourceTableUpdate(ctx, d, client); diags.HasError() {
					t.Fatalf("replicated ALTER: %v", diags)
				}
				if d.Get("uuid") != initialUUID {
					t.Fatalf("ALTER replaced table UUID: %q -> %q", initialUUID, d.Get("uuid"))
				}
				if got := len(d.Get("column").([]interface{})); got != 3 {
					t.Fatalf("column count after ALTER = %d", got)
				}
			}
			var count uint64
			if err := conn.QueryRow(ctx, "SELECT count() FROM "+qualified(resource)).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("row count after import/update = %d", count)
			}
		})
	}
}
