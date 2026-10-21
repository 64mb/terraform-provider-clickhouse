package provider_test

import (
	"context"
	"fmt"
	"net"
	"os"
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

func TestAccClusterLifecycle(t *testing.T) {
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
	}
	host, portText, err := net.SplitHostPort(addrs[0])
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := net.SplitHostPort(addrs[1]); err != nil {
		t.Fatal(err)
	}
	for _, engine := range []string{"MergeTree", "ReplicatedMergeTree", "Distributed"} {
		for _, mode := range []string{"default_cluster", "explicit_cluster", "macro_cluster"} {
			target := "test_cluster"
			if mode == "macro_cluster" {
				target = "'{cluster}'"
			}
			t.Run(engine+"/"+mode, func(t *testing.T) {
				name := fmt.Sprintf("terraform_cluster_%d", time.Now().UnixNano())
				qualified := "`" + name + "`.`events`"
				conns := make([]driver.Conn, 0, 2)
				uuids := make([]string, 2)
				config := func(updated bool) string {
					defaultCluster, cluster := target, ""
					if mode == "explicit_cluster" {
						defaultCluster, cluster = "missing_terraform_cluster", `cluster = "test_cluster"`
					}
					params := ""
					if engine == "ReplicatedMergeTree" {
						params = fmt.Sprintf(`engine_params = [%q, %q]`, "'/clickhouse/tables/{shard}/"+name+"'", "'{replica}'")
					}
					order, source := `order_by = ["id"]`, ""
					if engine == "Distributed" {
						order = ""
						params = `engine_params = ["'test_cluster'", "'${clickhouse_db.cluster.name}'", "'${clickhouse_table.source.name}'", "rand()"]`
						source = fmt.Sprintf(`resource "clickhouse_table" "source" {
 database = clickhouse_db.cluster.name
 name = "source"
 %s
 engine = "MergeTree"
 order_by = ["id"]
 column {
  name = "id"
  type = "UInt64"
 }
}`, cluster)
					}
					extra := ""
					if updated && engine != "Distributed" {
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
 default_cluster = %q
}
resource "clickhouse_db" "cluster" {
 name = %q
 %s
}
resource "clickhouse_table" "cluster" {
 database = clickhouse_db.cluster.name
 name = "events"
 %s
 engine = %q
 %s
 %s
 column {
  name = "id"
  type = "UInt64"
 }
 %s
}
%s
`, host, port, defaultCluster, name, cluster, cluster, engine, params, order, extra, source)
				}
				resource.Test(t, resource.TestCase{
					PreCheck: func() {
						for _, addr := range addrs {
							conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Username: "default"}, DialTimeout: 10 * time.Second})
							if err != nil {
								t.Fatal(err)
							}
							conns = append(conns, conn)
						}
						t.Cleanup(func() {
							for _, conn := range conns {
								ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
								if err := conn.Exec(ctx, "DROP DATABASE IF EXISTS `"+name+"` SYNC"); err != nil {
									t.Errorf("cleanup database: %v", err)
								}
								cancel()
								_ = conn.Close()
							}
						})
						for i, conn := range conns {
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							var shards, replicas uint64
							err := conn.QueryRow(ctx, "SELECT uniqExact(shard_num), count() FROM system.clusters WHERE cluster = 'test_cluster'").Scan(&shards, &replicas)
							cancel()
							if err != nil {
								t.Fatal(err)
							}
							if shards != 2 || replicas != 2 {
								t.Fatalf("node %d: expected two shards with one replica each, got %d shards and %d replicas", i, shards, replicas)
							}
						}
					},
					ProviderFactories: map[string]func() (*schema.Provider, error){"clickhouse": func() (*schema.Provider, error) { return provider.New("test")(), nil }},
					CheckDestroy: func(_ *terraform.State) error {
						for i, conn := range conns {
							ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
							var tables, databases uint64
							err := conn.QueryRow(ctx, "SELECT (SELECT count() FROM system.tables WHERE database = ?), (SELECT count() FROM system.databases WHERE name = ?)", name, name).Scan(&tables, &databases)
							cancel()
							if err != nil {
								return fmt.Errorf("node %d: %w", i, err)
							}
							if tables != 0 || databases != 0 {
								return fmt.Errorf("node %d: destroy left %d tables and %d databases", i, tables, databases)
							}
						}
						return nil
					},
					Steps: []resource.TestStep{
						{Config: config(false), Check: resource.ComposeTestCheckFunc(
							resource.TestCheckResourceAttr("clickhouse_db.cluster", "cluster", target),
							resource.TestCheckResourceAttr("clickhouse_table.cluster", "cluster", target),
							func(_ *terraform.State) error {
								for i, conn := range conns {
									ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
									err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = ? AND name = 'events'", name).Scan(&uuids[i])
									if err == nil {
										target := qualified
										if engine == "Distributed" {
											target = "`" + name + "`.`source`"
										}
										err = conn.Exec(ctx, fmt.Sprintf("INSERT INTO %s VALUES (%d)", target, i+1))
									}
									cancel()
									if err != nil {
										return fmt.Errorf("node %d: %w", i, err)
									}
								}
								return nil
							})},
						{Config: config(false), PlanOnly: true},
						{Config: config(true), Check: resource.ComposeTestCheckFunc(
							func(s *terraform.State) error {
								if engine == "Distributed" {
									return resource.TestCheckResourceAttr("clickhouse_table.cluster", "column.#", "1")(s)
								}
								return resource.TestCheckResourceAttr("clickhouse_table.cluster", "column.1.name", "extra")(s)
							},
							func(_ *terraform.State) error {
								for i, conn := range conns {
									ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
									var uuid string
									err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = ? AND name = 'events'", name).Scan(&uuid)
									var count, sum, nulls uint64
									if err == nil {
										if engine == "Distributed" {
											err = conn.QueryRow(ctx, "SELECT count(), sum(id), count() FROM "+qualified).Scan(&count, &sum, &nulls)
										} else {
											err = conn.QueryRow(ctx, "SELECT count(), sum(id), countIf(isNull(extra)) FROM "+qualified).Scan(&count, &sum, &nulls)
										}
									}
									cancel()
									if err != nil {
										return fmt.Errorf("node %d: %w", i, err)
									}
									expectedCount, expectedSum := uint64(1), uint64(i+1)
									if engine == "Distributed" {
										expectedCount, expectedSum = 2, 3
									}
									if uuid != uuids[i] || count != expectedCount || sum != expectedSum || nulls != expectedCount {
										return fmt.Errorf("node %d: ALTER changed UUID or data: uuid=%s, count=%d, sum=%d, nulls=%d", i, uuid, count, sum, nulls)
									}
								}
								return nil
							})},
						{Config: config(true), PlanOnly: true},
						{ResourceName: "clickhouse_table.cluster", ImportState: true, ImportStateId: target + ":" + name + ":events", ImportStateVerify: true},
						{Config: config(true), PlanOnly: true},
					},
				})
			})
		}
	}
}
