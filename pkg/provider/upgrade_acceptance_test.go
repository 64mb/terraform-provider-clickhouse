package provider

import (
	"context"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func TestAccProviderUpgradeFromV103(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run the released provider upgrade test")
	}
	address := os.Getenv("CLICKHOUSE_TEST_ADDR")
	if address == "" {
		t.Skip("set CLICKHOUSE_TEST_ADDR to a disposable ClickHouse native endpoint")
	}
	t.Setenv("TF_ACC_PROVIDER_HOST", "registry.opentofu.org")
	t.Setenv("TF_ACC_PROVIDER_NAMESPACE", "64mb")
	host, portString, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatal(err)
	}
	username := os.Getenv("TF_CLICKHOUSE_USERNAME")
	if username == "" {
		username = "default"
	}
	password := os.Getenv("TF_CLICKHOUSE_PASSWORD")
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{address}, Auth: clickhouse.Auth{Username: username, Password: password}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx := context.Background()
	name := fmt.Sprintf("upgrade_%d", time.Now().UnixNano())
	config := func(updated bool) string {
		extra := ""
		if updated {
			extra = `column {
 name = "label"
 type = "Nullable(String)"
}`
		}
		return fmt.Sprintf(`
terraform {
 required_providers {
  clickhouse = { source = "registry.opentofu.org/64mb/clickhouse" }
 }
}
provider "clickhouse" {
 host = %q
 port = %d
 username = %q
 password = %q
 secure = false
}
resource "clickhouse_db" "upgrade" { name = %q }
resource "clickhouse_table" "upgrade" {
 database = clickhouse_db.upgrade.name
 name = "records"
 engine = "MergeTree"
 order_by = ["id"]
 column {
  name = "id"
  type = "UInt64"
 }
 %s
}
resource "clickhouse_role" "database" {
 name = %q
 database = clickhouse_db.upgrade.name
 privileges = ["SELECT"]
}
resource "clickhouse_role" "sources" {
 name = %q
 database = "*"
 privileges = ["S3", "REMOTE"]
}
`, host, port, username, password, name, extra, name+"_database", name+"_sources")
	}
	ids := map[string]string{}
	var tableUUID, databaseUUID string
	capture := func(s *terraform.State) error {
		for key, rs := range s.RootModule().Resources {
			ids[key] = rs.Primary.ID
		}
		if err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = ? AND name = 'records'", name).Scan(&tableUUID); err != nil {
			return err
		}
		if err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.databases WHERE name = ?", name).Scan(&databaseUUID); err != nil {
			return err
		}
		return conn.Exec(ctx, "INSERT INTO `"+name+"`.`records` (id) VALUES (42)")
	}
	preserved := func(s *terraform.State) error {
		for key, id := range ids {
			rs := s.RootModule().Resources[key]
			if rs == nil || rs.Primary.ID != id {
				return fmt.Errorf("resource %s changed identity", key)
			}
		}
		var uuid string
		if err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = ? AND name = 'records'", name).Scan(&uuid); err != nil {
			return err
		}
		if uuid != tableUUID {
			return fmt.Errorf("table UUID changed from %s to %s", tableUUID, uuid)
		}
		if err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.databases WHERE name = ?", name).Scan(&uuid); err != nil {
			return err
		}
		if uuid != databaseUUID {
			return fmt.Errorf("database UUID changed from %s to %s", databaseUUID, uuid)
		}
		var value uint64
		if err := conn.QueryRow(ctx, "SELECT id FROM `"+name+"`.`records`").Scan(&value); err != nil {
			return err
		}
		if value != 42 {
			return fmt.Errorf("stored row changed to %d", value)
		}
		for _, role := range []string{"database", "sources"} {
			if count := s.RootModule().Resources["clickhouse_role."+role].Primary.Attributes["granted_privileges.#"]; count == "" || count == "0" {
				return fmt.Errorf("role %s lost recorded grants", role)
			}
		}
		if stateUUID := s.RootModule().Resources["clickhouse_table.upgrade"].Primary.Attributes["uuid"]; stateUUID != tableUUID {
			return fmt.Errorf("table state UUID changed to %s", stateUUID)
		}
		var grants uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM system.grants WHERE role_name = ? AND database = ? AND access_type = 'SELECT'", name+"_database", name).Scan(&grants); err != nil {
			return err
		}
		if grants != 1 {
			return fmt.Errorf("expected database SELECT grant, got %d", grants)
		}
		rows, err := conn.Query(ctx, "SHOW GRANTS FOR `"+name+"_sources`")
		if err != nil {
			return err
		}
		defer rows.Close()
		var sourceGrants string
		for rows.Next() {
			var grant string
			if err := rows.Scan(&grant); err != nil {
				return err
			}
			sourceGrants += grant + "\n"
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if !strings.Contains(sourceGrants, "S3") || !strings.Contains(sourceGrants, "REMOTE") {
			return fmt.Errorf("missing global source grants: %s", sourceGrants)
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
		CheckDestroy: func(_ *terraform.State) error {
			var count uint64
			if err := conn.QueryRow(ctx, "SELECT count() FROM system.databases WHERE name = ?", name).Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("database %s remains after destroy", name)
			}
			if err := conn.QueryRow(ctx, "SELECT count() FROM system.roles WHERE name IN (?, ?)", name+"_database", name+"_sources").Scan(&count); err != nil {
				return err
			}
			if count != 0 {
				return fmt.Errorf("upgrade roles remain after destroy")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config(false), ExternalProviders: map[string]resource.ExternalProvider{"clickhouse": {Source: "registry.opentofu.org/64mb/clickhouse", VersionConstraint: "1.0.3"}}, Check: capture},
			{Config: config(false), ProviderFactories: ProviderFactories, Check: preserved},
			{Config: config(false), ProviderFactories: ProviderFactories, PlanOnly: true},
			{Config: config(true), ProviderFactories: ProviderFactories, Check: resource.ComposeTestCheckFunc(preserved, resource.TestCheckResourceAttr("clickhouse_table.upgrade", "column.#", "2"))},
			{Config: config(true), ProviderFactories: ProviderFactories, PlanOnly: true},
		},
	})
}
