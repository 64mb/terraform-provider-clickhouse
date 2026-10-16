package resourcetable_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/provider"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestAccTableExternalImport(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC is not set")
	}
	ctx := context.Background()
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{"127.0.0.1:19000"}, Auth: clickhouse.Auth{Username: "default"}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	name := fmt.Sprintf("terraform_external_noop_%d", time.Now().UnixNano())
	if err := conn.Exec(ctx, "CREATE TABLE default."+name+" (id UInt64 COMMENT 'identifier', payload String DEFAULT toString(id), readable UInt64 ALIAS id + 1) ENGINE = ReplacingMergeTree ORDER BY id COMMENT 'external table'"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Exec(ctx, "DROP TABLE IF EXISTS default."+name); err != nil {
			t.Error(err)
		}
	}()
	config := fmt.Sprintf(`
provider "clickhouse" {
 host = "127.0.0.1"
 port = 19000
 username = "default"
 password = ""
}
resource "clickhouse_table" "external" {
 database = "default"
 name = %q
 engine = "ReplacingMergeTree"
 order_by = ["id"]
 primary_key = ["id"]
 comment = "external table"
 column {
  name = "id"
  type = "UInt64"
  comment = "identifier"
 }
 column {
  name = "payload"
  type = "String"
  default_kind = "DEFAULT"
  default_expression = "toString(id)"
 }
 column {
  name = "readable"
  type = "UInt64"
  default_kind = "ALIAS"
  default_expression = "id + 1"
 }
}
`, name)
	resource.Test(t, resource.TestCase{
		ProviderFactories: map[string]func() (*schema.Provider, error){"clickhouse": func() (*schema.Provider, error) { return provider.New("test")(), nil }},
		Steps: []resource.TestStep{
			{Config: config, ResourceName: "clickhouse_table.external", ImportState: true, ImportStateId: "default:" + name, ImportStatePersist: true},
			{Config: config, PlanOnly: true, Check: resource.ComposeTestCheckFunc(resource.TestCheckResourceAttr("clickhouse_table.external", "column.#", "3"), resource.TestCheckResourceAttr("clickhouse_table.external", "comment", "external table"))},
		},
	})
}
