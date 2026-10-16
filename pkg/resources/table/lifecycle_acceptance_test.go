package resourcetable_test

import (
	"context"
	"fmt"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/provider"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestAccTableLifecycle(t *testing.T) {
	name := fmt.Sprintf("terraform_lifecycle_%d", time.Now().UnixNano())
	config := func(updated bool) string {
		extra := ""
		comment := "initial"
		if updated {
			extra = `column {
 name = "extra"
 type = "Nullable(String)"
}`
			comment = "updated"
		}
		return fmt.Sprintf(`
provider "clickhouse" {
 host = "127.0.0.1"
 port = 19000
 username = "default"
 password = ""
 secure = false
}
resource "clickhouse_table" "lifecycle" {
 database = "default"
 name = %q
 engine = "MergeTree"
 order_by = ["id"]
 settings = { index_granularity = "4096" }
 comment = %q
 column {
  name = "id"
  type = "UInt64"
 }
 column {
  name = "label"
  type = "String"
  default_kind = "DEFAULT"
  default_expression = "'hello'"
 }
 %s
}
`, name, comment, extra)
	}
	resource.Test(t, resource.TestCase{
		ProviderFactories: map[string]func() (*schema.Provider, error){"clickhouse": func() (*schema.Provider, error) { return provider.New("test")(), nil }},
		Steps: []resource.TestStep{
			{Config: config(false), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("clickhouse_table.lifecycle", "column.#", "2"), resource.TestCheckResourceAttr("clickhouse_table.lifecycle", "comment", "initial"))},
			{Config: config(false), PlanOnly: true},
			{Config: config(true), Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("clickhouse_table.lifecycle", "column.#", "3"), resource.TestCheckResourceAttr("clickhouse_table.lifecycle", "column.2.name", "extra"), resource.TestCheckResourceAttr("clickhouse_table.lifecycle", "comment", "updated"))},
			{Config: config(true), PlanOnly: true},
			{ResourceName: "clickhouse_table.lifecycle", ImportState: true, ImportStateId: "default:" + name, ImportStateVerify: true},
			{Config: config(true), PlanOnly: true},
		},
	})
}

func TestAccTableComplexNoOp(t *testing.T) {
	name := fmt.Sprintf("terraform_complex_%d", time.Now().UnixNano())
	config := fmt.Sprintf(`
provider "clickhouse" {
 host = "127.0.0.1"
 port = 19000
 username = "default"
 password = ""
 secure = false
}
resource "clickhouse_table" "complex" {
 database = "default"
 name = %q
 engine = "MergeTree"
 order_by = ["id"]
 partition_by {
  by = "toYYYYMM(event_time)"
 }
 ttl = "event_time + INTERVAL 30 DAY"
 settings = { index_granularity = "4096" }
 column {
  name = "id"
  type = "UInt64"
 }
 column {
  name = "event_time"
  type = "DateTime"
  default_kind = "DEFAULT"
  default_expression = "now()"
 }
 column {
  name = "nested"
  type = "Nested(label String, value UInt64)"
 }
 column {
  name = "payload"
  type = "Tuple(name LowCardinality(String), values Array(Nullable(Decimal64(4))))"
 }
 column {
  name = "labels"
  type = "Map(String, Array(Nullable(UInt32)))"
 }
 column {
  name = "timestamp"
  type = "DateTime64(3, 'Europe/Moscow')"
  codec = "ZSTD(1)"
  ttl = "event_time + INTERVAL 90 DAY"
 }
 column {
  name = "status"
  type = "Enum16('ready' = 1000, 'done' = 2000)"
 }
}
`, name)
	resource.Test(t, resource.TestCase{
		ProviderFactories: map[string]func() (*schema.Provider, error){"clickhouse": func() (*schema.Provider, error) { return provider.New("test")(), nil }},
		Steps: []resource.TestStep{
			{Config: config, Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("clickhouse_table.complex", "column.#", "7"), resource.TestCheckResourceAttr("clickhouse_table.complex", "column.2.name", "nested"), resource.TestCheckResourceAttr("clickhouse_table.complex", "column.2.type", "Nested(label String, value UInt64)"), resource.TestCheckResourceAttr("clickhouse_table.complex", "partition_by.0.by", "toYYYYMM(event_time)"))},
			{Config: config, PlanOnly: true},
		},
	})
}

func TestAccTableRebuildLifecycle(t *testing.T) {
	name := fmt.Sprintf("terraform_rebuild_%d", time.Now().UnixNano())
	config := func(rebuild bool) string {
		order := `["id"]`
		block := ""
		if rebuild {
			order = `["label", "id"]`
			block = `rebuild {
 enabled = true
 maintenance_acknowledged = true
 operation_id = "sdk-rebuild"
}`
		}
		return fmt.Sprintf(`
provider "clickhouse" {
 host = "127.0.0.1"
 port = 19000
 username = "default"
 password = ""
 secure = false
}
resource "clickhouse_table" "rebuild" {
 database = "default"
 name = %q
 engine = "MergeTree"
 order_by = %s
 column {
 name = "id"
 type = "UInt64"
 }
 column {
 name = "label"
 type = "String"
 }
 %s
}
`, name, order, block)
	}
	var initialUUID, backup string
	connect := func() (driver.Conn, error) {
		return clickhouse.Open(&clickhouse.Options{Addr: []string{"127.0.0.1:19000"}, Auth: clickhouse.Auth{Username: "default"}})
	}
	t.Cleanup(func() {
		if backup != "" {
			conn, err := connect()
			if err == nil {
				defer conn.Close()
				_ = conn.Exec(context.Background(), "DROP TABLE IF EXISTS default.`"+backup+"`")
			}
		}
	})
	resource.Test(t, resource.TestCase{ProviderFactories: map[string]func() (*schema.Provider, error){"clickhouse": func() (*schema.Provider, error) { return provider.New("test")(), nil }}, Steps: []resource.TestStep{
		{Config: config(false), Check: func(s *terraform.State) error {
			initialUUID = s.RootModule().Resources["clickhouse_table.rebuild"].Primary.Attributes["uuid"]
			conn, err := connect()
			if err != nil {
				return err
			}
			defer conn.Close()
			return conn.Exec(context.Background(), fmt.Sprintf("INSERT INTO default.`%s` VALUES (1,'a'),(2,'b')", name))
		}},
		{Config: config(true), Check: func(s *terraform.State) error {
			attrs := s.RootModule().Resources["clickhouse_table.rebuild"].Primary.Attributes
			if attrs["uuid"] == initialUUID {
				return fmt.Errorf("rebuild did not replace UUID")
			}
			backup = attrs["rebuild_backup_table"]
			if backup == "" {
				return fmt.Errorf("backup missing")
			}
			conn, err := connect()
			if err != nil {
				return err
			}
			defer conn.Close()
			for _, table := range []string{name, backup} {
				var count, sum uint64
				if err := conn.QueryRow(context.Background(), "SELECT count(),sum(id) FROM default.`"+table+"`").Scan(&count, &sum); err != nil {
					return err
				}
				if count != 2 || sum != 3 {
					return fmt.Errorf("rows changed in %s", table)
				}
			}
			return nil
		}},
		{Config: config(true), PlanOnly: true},
		{ResourceName: "clickhouse_table.rebuild", ImportState: true, ImportStateId: "default:" + name, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"rebuild"}},
		{Config: config(true), PlanOnly: true},
	}})
}
