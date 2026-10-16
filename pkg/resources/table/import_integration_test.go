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

func TestIntegrationImportExternalTables(t *testing.T) {
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
	if err = conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	service := CHTableService{CHConnection: &conn}
	client := &common.ApiClient{ClickhouseConnection: &conn}
	for _, tc := range []struct {
		engine, args string
		params       []string
	}{
		{engine: "MergeTree"},
		{engine: "ReplacingMergeTree", args: "version", params: []string{"version"}},
		{engine: "SummingMergeTree", args: "(version)", params: []string{"version"}},
		{engine: "AggregatingMergeTree"},
		{engine: "CollapsingMergeTree", args: "sign", params: []string{"sign"}},
		{engine: "VersionedCollapsingMergeTree", args: "sign, version", params: []string{"sign", "version"}},
	} {
		t.Run(tc.engine, func(t *testing.T) {
			name := fmt.Sprintf("terraform_external_import_%d", time.Now().UnixNano())
			qualifiedName := quoteIdent("default") + "." + quoteIdent(name)
			comment := "external owner's table"
			if tc.engine == "ReplacingMergeTree" {
				comment = storedComment("legacy owner's table", "")
			}
			if tc.engine == "AggregatingMergeTree" {
				comment = `{"owner":"external","retention":30}`
			}
			sortingKey := "(id, version)"
			if tc.engine == "VersionedCollapsingMergeTree" {
				sortingKey = "id"
			}
			sql := "CREATE TABLE " + qualifiedName + ` (
 id UInt64 COMMENT 'external id' CODEC(Delta, ZSTD(1)),
 event_time DateTime DEFAULT now(),
 version UInt64,
 sign Int8,
 label String DEFAULT concat('a,b', toString(id)) COMMENT 'external label' TTL event_time + INTERVAL 7 DAY,
 derived UInt64 MATERIALIZED id + 1,
 readable UInt64 ALIAS id + 2
 ) ENGINE = ` + tc.engine + "(" + tc.args + ") PARTITION BY toYYYYMM(event_time) PRIMARY KEY id ORDER BY " + sortingKey + " SAMPLE BY id TTL event_time + INTERVAL 30 DAY SETTINGS index_granularity = 4096 COMMENT " + quoteString(comment)
			if err := conn.Exec(ctx, sql); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if e := conn.Exec(ctx, "DROP TABLE IF EXISTS "+qualifiedName); e != nil {
					t.Error(e)
				}
			}()
			d := schema.TestResourceDataRaw(t, ResourceTable().Schema, nil)
			d.SetId("default:" + name)
			if _, err := importTable(ctx, d, client); err != nil {
				t.Fatal(err)
			}
			if diags := resourceTableRead(ctx, d, client); diags.HasError() {
				t.Fatalf("import read: %v", diags)
			}
			if got := d.Get("engine"); got != tc.engine {
				t.Fatalf("engine = %v", got)
			}
			if got := d.Get("engine_params").([]interface{}); len(got) != len(tc.params) {
				t.Fatalf("engine params = %#v, want %#v", got, tc.params)
			} else {
				for i, p := range tc.params {
					if normalizeSQL(got[i].(string)) != normalizeSQL(p) {
						t.Fatalf("param = %v, want %s", got[i], p)
					}
				}
			}
			columns := d.Get("column").([]interface{})
			names := []string{}
			for _, col := range columns {
				names = append(names, col.(map[string]interface{})["name"].(string))
			}
			if !reflect.DeepEqual(names, []string{"id", "event_time", "version", "sign", "label", "derived", "readable"}) {
				t.Fatalf("physical order = %#v", names)
			}
			for _, expect := range []struct {
				index      int
				kind, expr string
			}{{1, "DEFAULT", "now()"}, {4, "DEFAULT", "concat('a,b', toString(id))"}, {5, "MATERIALIZED", "id + 1"}, {6, "ALIAS", "id + 2"}} {
				c := columns[expect.index].(map[string]interface{})
				if c["default_kind"] != expect.kind || normalizeSQL(c["default_expression"].(string)) != normalizeSQL(expect.expr) {
					t.Errorf("column expression = %#v", c)
				}
			}
			if columns[0].(map[string]interface{})["comment"] != "external id" || columns[4].(map[string]interface{})["comment"] != "external label" {
				t.Fatalf("column comments = %#v", columns)
			}
			if got := d.Get("order_by"); !reflect.DeepEqual(got, []interface{}{"id", "version"}) {
				t.Fatalf("sorting key = %#v", got)
			}
			if got := d.Get("primary_key"); !reflect.DeepEqual(got, []interface{}{"id"}) {
				t.Fatalf("primary key = %#v", got)
			}
			partitions := d.Get("partition_by").([]interface{})
			if len(partitions) != 1 {
				t.Fatalf("partition = %#v", partitions)
			}
			p := partitions[0].(map[string]interface{})
			expr := p["by"].(string)
			if fn := p["partition_function"].(string); fn != "" {
				expr = fn + "(" + expr + ")"
			}
			if normalizeSQL(expr) != "toYYYYMM(event_time)" {
				t.Fatalf("partition = %#v", p)
			}
			if got := d.Get("sample_by").(string); normalizeSQL(got) != "id" {
				t.Fatalf("sample_by = %q", got)
			}
			if got := d.Get("uuid").(string); got == "" || got == "00000000-0000-0000-0000-000000000000" {
				t.Fatalf("uuid = %q", got)
			}
			if got := d.Get("create_table_query").(string); !strings.Contains(got, name) || !strings.Contains(got, "CODEC") || !strings.Contains(got, "SAMPLE BY") {
				t.Fatalf("create_table_query = %q", got)
			}
			if got := columns[0].(map[string]interface{})["codec"].(string); normalizeSQL(got) != "Delta(8),ZSTD(1)" && normalizeSQL(got) != "Delta,ZSTD(1)" {
				t.Fatalf("codec = %q", got)
			}
			if got := columns[4].(map[string]interface{})["ttl"].(string); !strings.Contains(got, "event_time") || !strings.Contains(got, "7") {
				t.Fatalf("column TTL = %q", got)
			}
			if got := d.Get("ttl").(string); !strings.Contains(got, "event_time") || !strings.Contains(got, "30") {
				t.Fatalf("TTL = %q", got)
			}
			if got := d.Get("settings").(map[string]interface{})["index_granularity"]; got != "4096" {
				t.Fatalf("settings = %#v", d.Get("settings"))
			}
			wantComment := comment
			if tc.engine == "ReplacingMergeTree" {
				wantComment = "legacy owner's table"
			}
			if got := d.Get("comment"); got != wantComment {
				t.Fatalf("comment = %q, want %q", got, wantComment)
			}
			live, err := service.GetTable(ctx, "default", name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = live.ToResource(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestIntegrationImportMissingTable(t *testing.T) {
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
	d := schema.TestResourceDataRaw(t, ResourceTable().Schema, nil)
	d.SetId(fmt.Sprintf("default:terraform_missing_import_%d", time.Now().UnixNano()))
	if _, err = importTable(ctx, d, nil); err != nil {
		t.Fatal(err)
	}
	if diags := resourceTableRead(ctx, d, &common.ApiClient{ClickhouseConnection: &conn}); diags.HasError() {
		t.Fatalf("missing import read: %v", diags)
	}
	if d.Id() != "" {
		t.Fatalf("missing table left state ID %q", d.Id())
	}
}
