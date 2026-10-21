package resourcedb_test

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

func TestAccDatabaseExternalImport(t *testing.T) {
	addr := os.Getenv("CLICKHOUSE_TEST_ADDR")
	if addr == "" {
		addr = "127.0.0.1:19000"
	}
	for _, tc := range []struct{ name, stored, comment string }{
		{"empty", "", ""}, {"plain", `owner's "database" \ path`, `owner's "database" \ path`},
		{"json", `{"owner":"analytics"}`, `{"owner":"analytics"}`},
		{"null", `null`, `null`}, {"wrong_types", `{"comment":7,"cluster":false}`, `{"comment":7,"cluster":false}`},
		{"metadata", `{"comment":"owner's database","cluster":""}`, "owner's database"},
		{"legacy_metadata", `{"comment":"owner\'s database","cluster":""}`, "owner's database"},
		{"metadata_extra_fields", `{"comment":"owner's database","cluster":"","owner":"external"}`, "owner's database"},
	} {
		t.Run(tc.name, func(t *testing.T) { testDatabaseExternalImport(t, []string{addr}, tc.stored, tc.comment, "", "", "") })
	}
}

func TestAccClusterDatabaseExternalImport(t *testing.T) {
	raw := os.Getenv("CLICKHOUSE_CLUSTER_TEST_ADDRS")
	if raw == "" {
		t.Skip("set CLICKHOUSE_CLUSTER_TEST_ADDRS to the two native endpoints")
	}
	addrs := strings.Split(raw, ",")
	if len(addrs) != 2 {
		t.Fatal("expected two cluster endpoints")
	}
	t.Run("explicit", func(t *testing.T) {
		testDatabaseExternalImport(t, addrs, `{"comment":"external cluster database","cluster":"metadata_cluster"}`, "external cluster database", "missing_cluster", "test_cluster", "test_cluster")
	})
	t.Run("metadata", func(t *testing.T) {
		testDatabaseExternalImport(t, addrs, `{"comment":"external cluster database","cluster":"test_cluster"}`, "external cluster database", "missing_cluster", "", "test_cluster")
	})
	t.Run("default", func(t *testing.T) {
		testDatabaseExternalImport(t, addrs, "external cluster database", "external cluster database", "test_cluster", "", "test_cluster")
	})
}

func testDatabaseExternalImport(t *testing.T, addrs []string, stored, comment, defaultCluster, importCluster, wantCluster string) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC to run database import acceptance tests")
	}
	host, portText, err := net.SplitHostPort(strings.TrimSpace(addrs[0]))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("terraform import ' ` \\ %d", time.Now().UnixNano())
	quote := func(s string) string { return "`" + strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(s) + "`" }
	literal := func(s string) string { return "'" + strings.NewReplacer("\\", "\\\\", "'", "\\'").Replace(s) + "'" }
	var conns []driver.Conn
	var uuids, tableUUIDs []string
	t.Cleanup(func() {
		for _, conn := range conns {
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+quote(name)+" SYNC"); err != nil {
				t.Errorf("cleanup: %v", err)
			}
			cancel()
			_ = conn.Close()
		}
	})
	for _, addr := range addrs {
		conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{strings.TrimSpace(addr)}, Auth: clickhouse.Auth{Username: "default"}})
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := conn.Exec(ctx, "CREATE DATABASE "+quote(name)+" COMMENT "+literal(stored)); err != nil {
			t.Fatal(err)
		}
		if err := conn.Exec(ctx, "CREATE TABLE "+quote(name)+".retained (id UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
			t.Fatal(err)
		}
		if err := conn.Exec(ctx, "INSERT INTO "+quote(name)+".retained VALUES (42)"); err != nil {
			t.Fatal(err)
		}
		var uuid string
		if err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.databases WHERE name = ?", name).Scan(&uuid); err != nil {
			t.Fatal(err)
		}
		uuids = append(uuids, uuid)
		if err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = ? AND name = 'retained'", name).Scan(&uuid); err != nil {
			t.Fatal(err)
		}
		tableUUIDs = append(tableUUIDs, uuid)
	}
	config := fmt.Sprintf(`provider "clickhouse" {
 host = %q
 port = %d
 username = "default"
 password = ""
 secure = false
 default_cluster = %q
}
resource "clickhouse_db" "external" {
 name = %q
 comment = %q
}
`, host, port, defaultCluster, name, comment)
	id := name
	if importCluster != "" {
		id = importCluster + ":" + name
	}
	verify := func(_ *terraform.State) error {
		for i, conn := range conns {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			var uuid, current string
			err := conn.QueryRow(ctx, "SELECT toString(uuid), comment FROM system.databases WHERE name = ?", name).Scan(&uuid, &current)
			var tableUUID string
			if err == nil {
				err = conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = ? AND name = 'retained'", name).Scan(&tableUUID)
			}
			var value uint64
			if err == nil {
				err = conn.QueryRow(ctx, "SELECT id FROM "+quote(name)+".retained").Scan(&value)
			}
			cancel()
			if err != nil {
				return err
			}
			if uuid != uuids[i] || tableUUID != tableUUIDs[i] || current != stored || value != 42 {
				return fmt.Errorf("node %d: import changed UUID, comment or data", i)
			}
		}
		return nil
	}
	resource.Test(t, resource.TestCase{
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
			{Config: config, ResourceName: "clickhouse_db.external", ImportState: true, ImportStateId: id, ImportStatePersist: true},
			{Config: config, PlanOnly: true},
			{Config: config, Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("clickhouse_db.external", "cluster", wantCluster),
				resource.TestCheckResourceAttr("clickhouse_db.external", "comment", comment),
				verify,
				func(_ *terraform.State) error {
					for _, conn := range conns {
						ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						err := conn.Exec(ctx, "DROP TABLE "+quote(name)+".retained SYNC")
						cancel()
						if err != nil {
							return err
						}
					}
					return nil
				},
			)},
		},
	})
}

func TestAccDatabaseMissingImport(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC to run database import acceptance tests")
	}
	name := fmt.Sprintf("terraform_missing_import_%d", time.Now().UnixNano())
	config := fmt.Sprintf(`provider "clickhouse" {
 host = "127.0.0.1"
 port = 19000
 username = "default"
 password = ""
 secure = false
}
resource "clickhouse_db" "missing" {
 name = %q
}
`, name)
	resource.Test(t, resource.TestCase{
		ProviderFactories: map[string]func() (*schema.Provider, error){"clickhouse": func() (*schema.Provider, error) { return provider.New("test")(), nil }},
		Steps:             []resource.TestStep{{Config: config, ResourceName: "clickhouse_db.missing", ImportState: true, ImportStateId: name, ExpectError: regexp.MustCompile("Cannot import non-existent remote object")}},
	})
}
