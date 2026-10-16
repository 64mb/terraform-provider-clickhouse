package resourcetable

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

func TestIntegrationMetadataKeywordIdentifiers(t *testing.T) {
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
	service := CHTableService{CHConnection: &conn}
	for _, column := range []string{"ttl", "comment", "settings"} {
		for _, columnTTL := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_column_%t", column, columnTTL), func(t *testing.T) {
				name := fmt.Sprintf("terraform_keyword_%d", time.Now().UnixNano())
				qualifiedName := "default." + quoteIdent(name)
				def := "`ttl` DateTime, `comment` DateTime, `settings` DateTime, `expire` DateTime DEFAULT ttl + INTERVAL 2 DAY"
				tableTTL := " TTL " + column + " + INTERVAL 1 DAY"
				if columnTTL {
					def += " COMMENT 'metadata' TTL " + column + " + INTERVAL 1 DAY"
					tableTTL = ""
				}
				if err := conn.Exec(ctx, "CREATE TABLE "+qualifiedName+" ("+def+") ENGINE=MergeTree ORDER BY ttl"+tableTTL); err != nil {
					t.Fatal(err)
				}
				defer conn.Exec(ctx, "DROP TABLE IF EXISTS "+qualifiedName)
				live, err := service.GetTable(ctx, "default", name)
				if err != nil {
					t.Fatal(err)
				}
				resource, err := live.ToResource()
				if err != nil {
					t.Fatal(err)
				}
				expected := column + " + toIntervalDay(1)"
				if columnTTL {
					cols := resource.GetColumnsResourceList()
					if cols[3].TTL != expected {
						t.Fatalf("lost column TTL: %q from %s", cols[3].TTL, live.CreateTableQuery)
					}
				} else if resource.TTL != expected {
					t.Fatalf("lost table TTL: %q from %s", resource.TTL, live.CreateTableQuery)
				}
				if err := service.rebuildPreflight(ctx, live, *resource); err == nil || !strings.Contains(strings.ToLower(err.Error()), "ttl") {
					t.Fatalf("TTL table allowed rebuild: %v", err)
				}
				reread, err := service.GetTable(ctx, "default", name)
				if err != nil {
					t.Fatal(err)
				}
				after, err := reread.ToResource()
				if err != nil {
					t.Fatal(err)
				}
				plan, err := buildAlterPlan(*resource, *after, false)
				if err != nil || len(plan) > 0 {
					t.Fatalf("import roundtrip has changes: %#v, %v", plan, err)
				}
			})
		}
	}
}
