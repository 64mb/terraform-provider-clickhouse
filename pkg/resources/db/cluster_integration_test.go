package resourcedb

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func clusterDatabaseFixture(t *testing.T) (string, []driver.Conn, *common.ApiClient) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" || os.Getenv("CLICKHOUSE_CLUSTER_TEST_ADDRS") == "" {
		t.Skip("set TF_ACC and CLICKHOUSE_CLUSTER_TEST_ADDRS for two-node cluster tests")
	}
	addrs := strings.Split(os.Getenv("CLICKHOUSE_CLUSTER_TEST_ADDRS"), ",")
	if len(addrs) != 2 {
		t.Fatal("CLICKHOUSE_CLUSTER_TEST_ADDRS must contain exactly two endpoints")
	}
	name := fmt.Sprintf("terraform_cluster_db_%d", time.Now().UnixNano())
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
		conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{strings.TrimSpace(addr)}, Auth: clickhouse.Auth{Username: "default"}, Settings: clickhouse.Settings{"distributed_ddl_output_mode": "never_throw", "distributed_ddl_task_timeout": 0}, DialTimeout: 10 * time.Second})
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
	}
	return name, conns, &common.ApiClient{ClickhouseConnection: &conns[0], DefaultCluster: "test_cluster"}
}

func TestIntegrationClusterDatabaseRemoteCreateError(t *testing.T) {
	name, conns, client := clusterDatabaseFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := conns[1].Exec(ctx, "CREATE DATABASE `"+name+"`"); err != nil {
		t.Fatal(err)
	}
	r := ResourceDb()
	d := schema.TestResourceDataRaw(t, r.Schema, map[string]interface{}{"name": name})
	diags := r.CreateContext(ctx, d, client)
	if !diags.HasError() {
		t.Fatal("CREATE DATABASE must report a database conflict on the second node")
	}
	if !strings.Contains(strings.ToLower(fmt.Sprint(diags)), "already exists") {
		t.Fatalf("expected database conflict, got %v", diags)
	}
	if d.Id() != "" {
		t.Fatalf("failed create retained resource ID %q", d.Id())
	}
	for i, conn := range conns {
		var count uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM system.databases WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("node %d: expected partial CREATE to retain database, got %d", i, count)
		}
	}
}

func TestIntegrationClusterDatabaseRemoteTableDeleteGuard(t *testing.T) {
	name, conns, client := clusterDatabaseFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	r := ResourceDb()
	d := schema.TestResourceDataRaw(t, r.Schema, map[string]interface{}{"name": name})
	if diags := r.CreateContext(ctx, d, client); diags.HasError() {
		t.Fatalf("create: %v", diags)
	}
	if err := conns[1].Exec(ctx, "CREATE TABLE `"+name+"`.remote_only (id UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	if err := conns[1].Exec(ctx, "INSERT INTO `"+name+"`.remote_only VALUES (42)"); err != nil {
		t.Fatal(err)
	}
	id := d.Id()
	diags := r.DeleteContext(ctx, d, client)
	if !diags.HasError() {
		t.Fatal("DROP DATABASE must reject a table present only on the second node")
	}
	if d.Id() != id {
		t.Fatalf("failed delete changed resource ID from %q to %q", id, d.Id())
	}
	for i, conn := range conns {
		var count uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM system.databases WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("node %d: failed delete removed database", i)
		}
	}
	var value uint64
	if err := conns[1].QueryRow(ctx, "SELECT id FROM `"+name+"`.remote_only").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != 42 {
		t.Fatalf("failed delete changed remote table data: %d", value)
	}
	if err := conns[1].Exec(ctx, "DROP TABLE `"+name+"`.remote_only SYNC"); err != nil {
		t.Fatal(err)
	}
	if diags := r.DeleteContext(ctx, d, client); diags.HasError() {
		t.Fatalf("delete empty cluster database: %v", diags)
	}
	for i, conn := range conns {
		var count uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM system.databases WHERE name = ?", name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("node %d: successful delete retained database", i)
		}
	}
}

func TestIntegrationClusterDatabaseUnavailable(t *testing.T) {
	addr := os.Getenv("CLICKHOUSE_CLUSTER_OFFLINE_TEST_ADDR")
	if addr == "" {
		t.Skip("set CLICKHOUSE_CLUSTER_OFFLINE_TEST_ADDR while the second cluster node is stopped")
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr:        []string{addr},
		Auth:        clickhouse.Auth{Username: "default"},
		DialTimeout: time.Second,
		Settings:    clickhouse.Settings{"skip_unavailable_shards": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("terraform_cluster_offline_db_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := conn.Exec(ctx, "DROP DATABASE IF EXISTS `"+name+"` SYNC"); err != nil {
			t.Errorf("cleanup: %v", err)
		}
		_ = conn.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	err = conn.Exec(ctx, "CREATE DATABASE `"+name+"`")
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	r := ResourceDb()
	d := schema.TestResourceDataRaw(t, r.Schema, map[string]interface{}{"name": name, "cluster": "test_cluster"})
	id := "test_cluster:" + name
	d.SetId(id)
	client := &common.ApiClient{ClickhouseConnection: &conn}
	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	diags := r.DeleteContext(ctx, d, client)
	cancel()
	if !diags.HasError() {
		t.Fatal("DROP DATABASE must fail when a cluster node is unavailable even with skip_unavailable_shards enabled")
	}
	if d.Id() != id {
		t.Fatalf("failed delete changed resource ID from %q to %q", id, d.Id())
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var count uint64
	if err := conn.QueryRow(ctx, "SELECT count() FROM system.databases WHERE name = ?", name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("failed cluster guard removed the local database")
	}
}
