package provider_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/provider"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func TestAccClusterReplicationMacros(t *testing.T) {
	raw := os.Getenv("CLICKHOUSE_CLUSTER_TEST_ADDRS")
	if raw == "" {
		t.Skip("set CLICKHOUSE_CLUSTER_TEST_ADDRS to the two native cluster endpoints")
	}
	addrs := strings.Split(raw, ",")
	if len(addrs) != 2 {
		t.Fatal("CLICKHOUSE_CLUSTER_TEST_ADDRS must contain exactly two host:port endpoints")
	}
	for i := range addrs {
		addrs[i] = strings.TrimSpace(addrs[i])
		if _, _, err := net.SplitHostPort(addrs[i]); err != nil {
			t.Fatal(err)
		}
	}
	host, portText, _ := net.SplitHostPort(addrs[0])
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"ReplicatedMergeTree", "ReplicatedReplacingMergeTree"} {
		for _, macros := range []string{"database", "table", "both"} {
			t.Run(engine+"/"+macros, func(t *testing.T) {
				name := fmt.Sprintf("terraform_macros_%d", time.Now().UnixNano())
				dbPath, tablePath := name, "events"
				if macros == "database" || macros == "both" {
					dbPath = "{database}"
				}
				if macros == "table" || macros == "both" {
					tablePath = "{table}"
				}
				path := "'/terraform/macros/" + dbPath + "/" + tablePath + "/{shard}'"
				concrete := "'/terraform/macros/" + name + "/events/{shard}'"
				qualified := "`" + name + "`.`events`"
				var conns []driver.Conn
				uuids := make([]string, 2)
				config := func(updated, changedPath bool) string {
					enginePath := path
					if changedPath {
						enginePath = strings.TrimSuffix(path, "'") + "/changed'"
					}
					comment, extra := "initial", ""
					if updated {
						comment = "updated"
						extra = `column {
 name = "extra"
 type = "Nullable(String)"
}`
					}
					return fmt.Sprintf(`
provider "clickhouse" {
 host = %q
 port = %d
 username = "default"
 password = ""
 secure = false
 default_cluster = "test_cluster"
}
resource "clickhouse_db" "macros" { name = %q }
resource "clickhouse_table" "macros" {
 database = clickhouse_db.macros.name
 name = "events"
 engine = %q
 engine_params = [%q, "'{replica}'"]
 order_by = ["id"]
 comment = %q
 column {
 name = "id"
 type = "UInt64"
 }
 %s
}
`, host, port, name, engine, enginePath, comment, extra)
				}
				preserved := func(_ *terraform.State) error {
					for i, conn := range conns {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						var uuid string
						var count, sum, nulls uint64
						err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = ? AND name = 'events'", name).Scan(&uuid)
						if err == nil {
							err = conn.QueryRow(ctx, "SELECT count(), sum(id), countIf(isNull(extra)) FROM "+qualified).Scan(&count, &sum, &nulls)
						}
						cancel()
						if err != nil {
							return fmt.Errorf("node %d: %w", i, err)
						}
						if uuid != uuids[i] || count != 1 || sum != uint64(i+1) || nulls != 1 {
							return fmt.Errorf("node %d: UUID or data changed: uuid=%s count=%d sum=%d nulls=%d", i, uuid, count, sum, nulls)
						}
					}
					return nil
				}
				resource.Test(t, resource.TestCase{
					PreCheck: func() {
						for _, addr := range addrs {
							conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Username: "default"}, DialTimeout: 10 * time.Second})
							if err != nil {
								t.Fatal(err)
							}
							conns = append(conns, conn)
							t.Cleanup(func() {
								ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
								defer cancel()
								if err := conn.Exec(ctx, "DROP DATABASE IF EXISTS `"+name+"` SYNC"); err != nil {
									t.Errorf("cleanup database: %v", err)
								}
								_ = conn.Close()
							})
						}
					},
					ProviderFactories: map[string]func() (*schema.Provider, error){"clickhouse": func() (*schema.Provider, error) { return provider.New("test")(), nil }},
					CheckDestroy: func(_ *terraform.State) error {
						for i, conn := range conns {
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							var count uint64
							err := conn.QueryRow(ctx, "SELECT count() FROM system.databases WHERE name = ?", name).Scan(&count)
							cancel()
							if err != nil {
								return err
							}
							if count != 0 {
								return fmt.Errorf("node %d: database remains after destroy", i)
							}
						}
						return nil
					},
					Steps: []resource.TestStep{
						{Config: config(false, false), Check: func(_ *terraform.State) error {
							for i, conn := range conns {
								ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
								var full string
								err := conn.QueryRow(ctx, "SELECT toString(uuid), engine_full FROM system.tables WHERE database = ? AND name = 'events'", name).Scan(&uuids[i], &full)
								if err == nil && (strings.Contains(full, "{database}") || strings.Contains(full, "{table}")) {
									err = fmt.Errorf("ClickHouse did not materialize macros: %s", full)
								}
								if err == nil {
									err = conn.Exec(ctx, fmt.Sprintf("INSERT INTO %s VALUES (%d)", qualified, i+1))
								}
								cancel()
								if err != nil {
									return fmt.Errorf("node %d: %w", i, err)
								}
							}
							return nil
						}},
						{Config: config(false, false), PlanOnly: true},
						{ResourceName: "clickhouse_table.macros", ImportState: true, ImportStateId: "test_cluster:" + name + ":events", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"engine_params"}, ImportStateCheck: func(states []*terraform.InstanceState) error {
							for _, state := range states {
								if state.Ephemeral.Type != "clickhouse_table" {
									continue
								}
								if got := state.Attributes["engine_params.0"]; got != concrete {
									return fmt.Errorf("imported path = %q, want %q", got, concrete)
								}
								return nil
							}
							return fmt.Errorf("imported table missing from state")
						}},
						{Config: config(false, false), PlanOnly: true},
						{Config: config(true, false), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("clickhouse_table.macros", "comment", "updated"), resource.TestCheckResourceAttr("clickhouse_table.macros", "column.1.name", "extra"), preserved)},
						{Config: config(true, false), PlanOnly: true},
						{Config: config(true, true), PlanOnly: true, ExpectNonEmptyPlan: true},
						{Config: strings.Replace(config(true, false), "'{replica}'", "'changed_replica'", 1), PlanOnly: true, ExpectNonEmptyPlan: true},
						{Config: config(true, true), ExpectError: regexp.MustCompile("engine.*maintenance rebuild")},
						{Config: config(true, false), Check: preserved},
					},
				})
				path = strings.Replace(path, "/terraform/macros/", "/terraform/import_macros/", 1)
				concrete = strings.Replace(concrete, "/terraform/macros/", "/terraform/import_macros/", 1)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := conns[0].Exec(ctx, "CREATE DATABASE `"+name+"` ON CLUSTER test_cluster COMMENT '{\"cluster\":\"test_cluster\",\"comment\":\"\"}'")
				if err == nil {
					err = conns[0].Exec(ctx, "CREATE TABLE "+qualified+" ON CLUSTER test_cluster (id UInt64) ENGINE = "+engine+"("+path+", '{replica}') ORDER BY id COMMENT '{\"cluster\":\"test_cluster\",\"comment\":\"initial\"}'")
				}
				cancel()
				if err != nil {
					t.Fatal(err)
				}
				resource.Test(t, resource.TestCase{
					CheckDestroy: func(_ *terraform.State) error {
						for i, conn := range conns {
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							var count uint64
							err := conn.QueryRow(ctx, "SELECT count() FROM system.databases WHERE name = ?", name).Scan(&count)
							cancel()
							if err != nil {
								return err
							}
							if count != 0 {
								return fmt.Errorf("node %d: imported database remains after destroy", i)
							}
						}
						return nil
					},
					ProviderFactories: map[string]func() (*schema.Provider, error){"clickhouse": func() (*schema.Provider, error) { return provider.New("test")(), nil }},
					Steps: []resource.TestStep{
						{Config: config(false, false), ResourceName: "clickhouse_db.macros", ImportState: true, ImportStateId: "test_cluster:" + name, ImportStatePersist: true},
						{Config: config(false, false), ResourceName: "clickhouse_table.macros", ImportState: true, ImportStateId: "test_cluster:" + name + ":events", ImportStatePersist: true, ImportStateCheck: func(states []*terraform.InstanceState) error {
							for _, state := range states {
								if state.Ephemeral.Type == "clickhouse_table" {
									if got := state.Attributes["engine_params.0"]; got != concrete {
										return fmt.Errorf("imported path = %q, want %q", got, concrete)
									}
									return nil
								}
							}
							return fmt.Errorf("imported table missing from state")
						}},
						{Config: config(false, false), PlanOnly: true},
					},
				})
			})
		}
	}
}
