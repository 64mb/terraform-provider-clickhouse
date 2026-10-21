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

func clusterErrorFixture(t *testing.T) (string, string, []driver.Conn) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC to run cluster acceptance tests")
	}
	raw := os.Getenv("CLICKHOUSE_CLUSTER_TEST_ADDRS")
	if raw == "" {
		t.Skip("set CLICKHOUSE_CLUSTER_TEST_ADDRS to two native cluster endpoints")
	}
	addrs := strings.Split(raw, ",")
	if len(addrs) != 2 {
		t.Fatal("CLICKHOUSE_CLUSTER_TEST_ADDRS must contain exactly two endpoints")
	}
	host, portText, err := net.SplitHostPort(strings.TrimSpace(addrs[0]))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("terraform_cluster_errors_%d", time.Now().UnixNano())
	var conns []driver.Conn
	t.Cleanup(func() {
		for _, conn := range conns {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := conn.Exec(ctx, "DROP DATABASE IF EXISTS `"+name+"` SYNC"); err != nil {
				t.Errorf("cleanup: %v", err)
			}
			cancel()
			_ = conn.Close()
		}
	})
	for _, addr := range addrs {
		conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{strings.TrimSpace(addr)}, Auth: clickhouse.Auth{Username: "default"}, DialTimeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err = conn.Exec(ctx, "CREATE DATABASE `"+name+"`")
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	config := fmt.Sprintf(`provider "clickhouse" {
 host = %q
 port = %d
 username = "default"
 password = ""
 secure = false
 default_cluster = "test_cluster"
}
`, host, port)
	return config, name, conns
}

func clusterErrorProviders() map[string]func() (*schema.Provider, error) {
	return map[string]func() (*schema.Provider, error){"clickhouse": func() (*schema.Provider, error) { return provider.New("test")(), nil }}
}

func TestAccClusterRemoteDDLError(t *testing.T) {
	config, name, conns := clusterErrorFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err := conns[1].Exec(ctx, "CREATE TABLE `"+name+"`.events (conflicting String) ENGINE = MergeTree ORDER BY conflicting")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	config += fmt.Sprintf(`resource "clickhouse_table" "conflict" {
 database = %q
 name = "events"
 engine = "MergeTree"
 order_by = ["id"]
 column {
  name = "id"
  type = "UInt64"
 }
}
`, name)
	resource.Test(t, resource.TestCase{ProviderFactories: clusterErrorProviders(), Steps: []resource.TestStep{
		{Config: config, ExpectError: regexp.MustCompile(`(?i)(already exists|TABLE_ALREADY_EXISTS)`)},
	}})
}

func TestAccClusterMissingCluster(t *testing.T) {
	config, name, _ := clusterErrorFixture(t)
	config += fmt.Sprintf(`resource "clickhouse_table" "missing" {
 database = %q
 name = "events"
 cluster = "missing_terraform_cluster"
 engine = "MergeTree"
 order_by = ["id"]
 column {
  name = "id"
  type = "UInt64"
 }
}
`, name)
	resource.Test(t, resource.TestCase{ProviderFactories: clusterErrorProviders(), Steps: []resource.TestStep{
		{Config: config, ExpectError: regexp.MustCompile(`(?i)(not found|does not exist|UNKNOWN_CLUSTER)`)},
	}})
}

func TestAccClusterRemoteAlterError(t *testing.T) {
	config, name, conns := clusterErrorFixture(t)
	base := config + fmt.Sprintf(`resource "clickhouse_table" "alter" {
 database = %q
 name = "events"
 engine = "MergeTree"
 order_by = ["id"]
 column {
  name = "id"
  type = "UInt64"
 }
 %%s
}
`, name)
	initial := fmt.Sprintf(base, "")
	updated := fmt.Sprintf(base, `column {
 name = "extra"
 type = "String"
}`)
	resource.Test(t, resource.TestCase{ProviderFactories: clusterErrorProviders(), Steps: []resource.TestStep{
		{Config: initial, Check: func(_ *terraform.State) error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return conns[1].Exec(ctx, "ALTER TABLE `"+name+"`.events ADD COLUMN extra String")
		}},
		{Config: updated, ExpectError: regexp.MustCompile(`(?i)(already exists|DUPLICATE_COLUMN)`)},
	}})
}

func TestAccClusterDistributedAlterRejected(t *testing.T) {
	config, name, conns := clusterErrorFixture(t)
	for _, conn := range conns {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := conn.Exec(ctx, "CREATE TABLE `"+name+"`.local (id UInt64) ENGINE = MergeTree ORDER BY id")
		cancel()
		if err != nil {
			t.Fatal(err)
		}
	}
	base := config + fmt.Sprintf(`resource "clickhouse_table" "alter" {
 database = %q
 name = "events"
 engine = "Distributed"
 engine_params = ["'test_cluster'", %q, "'local'", "rand()"]
 column {
  name = "id"
  type = "UInt64"
 }
 %%s
}
`, name, "'"+name+"'")
	resource.Test(t, resource.TestCase{ProviderFactories: clusterErrorProviders(), Steps: []resource.TestStep{
		{Config: fmt.Sprintf(base, "")},
		{Config: fmt.Sprintf(base, `column {
 name = "extra"
 type = "String"
}`), ExpectError: regexp.MustCompile("Distributed definitions require an external topology migration")},
	}})
}
