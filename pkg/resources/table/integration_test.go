package resourcetable

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestIntegrationTableRoundTrip(t *testing.T) {
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
	if err := conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("terraform_roundtrip_%d", time.Now().UnixNano())
	client := &common.ApiClient{ClickhouseConnection: &conn}
	data := schema.TestResourceDataRaw(t, ResourceTable().Schema, map[string]interface{}{
		"database": "default", "name": name, "engine": "MergeTree", "order_by": []interface{}{"id"},
		"ttl": "event_time + INTERVAL 30 DAY", "settings": map[string]interface{}{"index_granularity": "4096"},
		"comment": "owner's table; DROP TABLE never_run", "column": []interface{}{
			map[string]interface{}{"name": "id", "type": "UInt64"},
			map[string]interface{}{"name": "event_time", "type": "DateTime", "default_kind": "DEFAULT", "default_expression": "now()"},
			map[string]interface{}{"name": "quoted`field", "type": "String", "default_kind": "DEFAULT", "default_expression": "'it\\'s safe'", "comment": "column's comment; harmless"},
			map[string]interface{}{"name": "payload", "type": "Tuple(label String, value Decimal(18, 4))"},
			map[string]interface{}{"name": "labels", "type": "Map(String, Array(Nullable(UInt32)))"},
			map[string]interface{}{"name": "status", "type": "Enum8('new' = 1, 'done' = 2)"},
		},
	})
	t.Cleanup(func() { _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+quoteIdent("default")+"."+quoteIdent(name)) })
	if diags := resourceTableCreate(ctx, data, client); diags.HasError() {
		t.Fatalf("create: %v", diags)
	}
	if diags := resourceTableRead(ctx, data, client); diags.HasError() {
		t.Fatalf("read: %v", diags)
	}
	if got := data.Get("comment"); got != "owner's table; DROP TABLE never_run" {
		t.Fatalf("comment: %q", got)
	}
	columns := data.Get("column").([]interface{})
	if len(columns) != 6 {
		t.Fatalf("columns: %#v", columns)
	}
	quoted := columns[2].(map[string]interface{})
	if quoted["name"] != "quoted`field" || quoted["comment"] != "column's comment; harmless" {
		t.Fatalf("quoted column: %#v", quoted)
	}
	if quoted["default_kind"] != "DEFAULT" || !strings.Contains(quoted["default_expression"].(string), "safe") {
		t.Fatalf("default: %#v", quoted)
	}
	if got := data.Get("settings").(map[string]interface{})["index_granularity"]; got != "4096" {
		t.Fatalf("settings: %#v", data.Get("settings"))
	}
	if got := data.Get("ttl").(string); !strings.Contains(got, "event_time") || !strings.Contains(got, "30") {
		t.Fatalf("ttl: %q", got)
	}
	service := CHTableService{CHConnection: &conn}
	table, err := service.GetTable(ctx, "default", name)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"UInt64", "DateTime", "String", "Tuple(label String, value Decimal(18, 4))", "Map(String, Array(Nullable(UInt32)))", "Enum8('new' = 1, 'done' = 2)"} {
		actual, e := CanonicalType(table.Columns[i].Type)
		if e != nil {
			t.Fatal(e)
		}
		expected, e := CanonicalType(want)
		if e != nil {
			t.Fatal(e)
		}
		if actual != expected {
			t.Errorf("column %d type %q, want %q", i, actual, expected)
		}
	}
	if diags := resourceTableDelete(ctx, data, client); diags.HasError() {
		t.Fatalf("delete: %v", diags)
	}
	if data.Id() != "" {
		t.Fatalf("delete left ID %q", data.Id())
	}
}

func TestIntegrationTypeMatrix(t *testing.T) {
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
	types := []string{"UInt64", "LowCardinality(Nullable(String))", "Nullable(Int32)", "Array(Tuple(name String, value Nullable(UInt64)))", "Tuple(label String, amount Decimal(18, 4))", "Map(String, Array(Nullable(UInt32)))", "Nested(name String, amount UInt64)", "Decimal32(3)", "Decimal64(6)", "Decimal128(9)", "DateTime64(3, 'Europe/Moscow')", "Enum8('new' = 1, 'done' = 2)", "Enum16('ready' = 1000, 'done' = 2000)", "FixedString(16)", "UUID", "IPv4", "IPv6"}
	columns := []interface{}{}
	for i, typ := range types {
		columns = append(columns, map[string]interface{}{"name": fmt.Sprintf("c%d", i), "type": typ})
	}
	table := TableResource{Database: "default", Name: fmt.Sprintf("tf_types_%d", time.Now().UnixNano()), Engine: "MergeTree", OrderBy: []string{"c0"}, Columns: columns}
	service := CHTableService{CHConnection: &conn}
	if err := service.CreateTable(ctx, table); err != nil {
		t.Fatal(err)
	}
	defer service.DeleteTable(ctx, table)
	live, err := service.GetTable(ctx, table.Database, table.Name)
	if err != nil {
		t.Fatal(err)
	}
	actual := restoreNestedColumns(table.Columns, live.ColumnsToResource())
	if len(actual) != len(columns) {
		t.Fatalf("column count %d, want %d", len(actual), len(columns))
	}
	for i, c := range actual {
		got, err := CanonicalType(c.(map[string]interface{})["type"].(string))
		if err != nil {
			t.Fatal(err)
		}
		want, err := CanonicalType(types[i])
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("column %d: %q, want %q", i, got, want)
		}
	}
}

func TestIntegrationAlterPreservesRows(t *testing.T) {
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
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"mutations_sync": 2, "alter_sync": 2}))
	table := TableResource{Database: "default", Name: fmt.Sprintf("tf_alter_%d", time.Now().UnixNano()), Engine: "MergeTree", OrderBy: []string{"id"}, PrimaryKey: []string{"id"}, Comment: "initial", TTL: "event_time + INTERVAL 30 DAY", Settings: map[string]string{"index_granularity": "8192", "min_bytes_for_wide_part": "4096"}, Columns: []interface{}{map[string]interface{}{"name": "id", "type": "UInt64"}, map[string]interface{}{"name": "value", "type": "UInt32", "default_kind": "DEFAULT", "default_expression": "7", "comment": "initial"}, map[string]interface{}{"name": "event_time", "type": "DateTime"}, map[string]interface{}{"name": "obsolete", "type": "String"}}}
	service := CHTableService{CHConnection: &conn}
	if err := service.CreateTable(ctx, table); err != nil {
		t.Fatal(err)
	}
	defer service.DeleteTable(ctx, table)
	if err := conn.Exec(ctx, "INSERT INTO "+qualified(table)+" (id,value,event_time,obsolete) VALUES (1,11,now(),'x'),(2,22,now(),'y')"); err != nil {
		t.Fatal(err)
	}
	initial, err := service.GetTable(ctx, table.Database, table.Name)
	if err != nil {
		t.Fatal(err)
	}
	next := table
	next.Comment = ""
	next.TTL = "event_time + INTERVAL 60 DAY"
	next.Settings = map[string]string{"index_granularity": "8192"}
	next.OrderBy = []string{"id", "added"}
	next.Columns = []interface{}{map[string]interface{}{"name": "value", "type": "UInt64", "codec": "ZSTD(1)", "ttl": "event_time + INTERVAL 90 DAY"}, map[string]interface{}{"name": "id", "type": "UInt64"}, map[string]interface{}{"name": "event_time", "type": "DateTime"}, map[string]interface{}{"name": "added", "type": "UInt32"}}
	if _, err := buildAlterPlan(table, next, false); err == nil {
		t.Fatal("drop did not require acknowledgement")
	}
	apply := func(old, next TableResource) {
		actions, err := buildAlterPlan(old, next, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.executeAlterPlan(ctx, next, actions); err != nil {
			t.Fatalf("%s: %v", strings.Join(actions, ", "), err)
		}
	}
	apply(table, next)
	live, err := service.GetTable(ctx, table.Database, table.Name)
	if err != nil {
		t.Fatal(err)
	}
	if live.UUID != initial.UUID {
		t.Fatal("ALTER replaced table")
	}
	if len(live.Columns) != 4 || live.Columns[0].Name != "value" || live.Columns[0].Type != "UInt64" || live.Columns[0].DefaultKind != "" || live.Columns[0].Comment != "" || live.Columns[3].Name != "added" {
		t.Fatalf("altered columns: %#v", live.Columns)
	}
	refreshed, err := live.ToResource()
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Comment != "" || !strings.Contains(refreshed.TTL, "60") || refreshed.Settings["min_bytes_for_wide_part"] != "" {
		t.Fatalf("altered metadata: %#v", refreshed)
	}
	if codecArguments(live.Columns[0].Codec) != "ZSTD(1)" || !strings.Contains(optionalColumnString(refreshed.Columns[0], "ttl"), "90") {
		t.Fatalf("codec or column TTL not applied: %#v", refreshed.Columns[0])
	}
	removed := next
	removed.TTL = ""
	removed.Columns = append([]interface{}{}, next.Columns...)
	removed.Columns[0] = map[string]interface{}{"name": "value", "type": "UInt64"}
	apply(next, removed)
	live, err = service.GetTable(ctx, table.Database, table.Name)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err = live.ToResource()
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.TTL != "" || optionalColumnString(refreshed.Columns[0], "ttl") != "" || live.Columns[0].Codec != "" && live.Columns[0].Codec != "CODEC(Default)" {
		t.Fatalf("TTL remains: %q", refreshed.TTL)
	}
	var count, sum, added uint64
	if err := conn.QueryRow(ctx, "SELECT count(),sum(value),sum(added) FROM "+qualified(table)).Scan(&count, &sum, &added); err != nil {
		t.Fatal(err)
	}
	if count != 2 || sum != 33 || added != 0 || live.UUID != initial.UUID {
		t.Fatalf("rows changed: count=%d sum=%d added=%d", count, sum, added)
	}
}
