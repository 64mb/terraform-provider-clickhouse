package resourcetable

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func clusterDDLTestConnection(t *testing.T, addr string) driver.Conn {
	t.Helper()
	if _, _, err := net.SplitHostPort(addr); err != nil {
		t.Fatal(err)
	}
	conn, err := clickhouse.Open(&clickhouse.Options{
		Addr: []string{addr}, Auth: clickhouse.Auth{Username: "default"}, DialTimeout: 5 * time.Second,
		Settings: clickhouse.Settings{"distributed_ddl_output_mode": "never_throw", "distributed_ddl_task_timeout": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	return conn
}

func clusterDDLTestData(t *testing.T, name string) *schema.ResourceData {
	t.Helper()
	return schema.TestResourceDataRaw(t, ResourceTable().Schema, map[string]interface{}{
		"database": "default", "name": name, "cluster": "test_cluster", "engine": "MergeTree",
		"order_by": []interface{}{"id"}, "column": []interface{}{map[string]interface{}{"name": "id", "type": "UInt64"}},
	})
}

func cleanupClusterDDLTestTable(t *testing.T, conn driver.Conn, name string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := conn.Exec(ctx, "DROP TABLE IF EXISTS default."+quoteIdent(name)+" SYNC"); err != nil {
			t.Errorf("cleanup table %s: %v", name, err)
		}
	})
}

func TestIntegrationClusterRemoteDDLFailure(t *testing.T) {
	raw := os.Getenv("CLICKHOUSE_CLUSTER_TEST_ADDRS")
	if raw == "" {
		t.Skip("CLICKHOUSE_CLUSTER_TEST_ADDRS is not set")
	}
	addrs := strings.Split(raw, ",")
	if len(addrs) != 2 {
		t.Fatal("CLICKHOUSE_CLUSTER_TEST_ADDRS must contain exactly two host:port endpoints")
	}
	first := clusterDDLTestConnection(t, strings.TrimSpace(addrs[0]))
	second := clusterDDLTestConnection(t, strings.TrimSpace(addrs[1]))
	name := fmt.Sprintf("terraform_remote_ddl_failure_%d", time.Now().UnixNano())
	cleanupClusterDDLTestTable(t, first, name)
	cleanupClusterDDLTestTable(t, second, name)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := second.Exec(ctx, "CREATE TABLE default."+quoteIdent(name)+" (id UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	var originalUUID string
	if err := second.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = 'default' AND name = ?", name).Scan(&originalUUID); err != nil {
		t.Fatal(err)
	}
	data := clusterDDLTestData(t, name)
	client := &common.ApiClient{ClickhouseConnection: &first}
	diags := resourceTableCreate(ctx, data, client)
	if !diags.HasError() {
		t.Errorf("create reported success despite a remote node already owning the table: %v", diags)
	}
	if data.Id() != "" {
		t.Errorf("failed cluster create retained resource ID %q", data.Id())
	}
	var currentUUID string
	if err := second.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = 'default' AND name = ?", name).Scan(&currentUUID); err != nil {
		t.Fatal(err)
	}
	if currentUUID != originalUUID {
		t.Errorf("failed create replaced the preexisting remote table: UUID %s became %s", originalUUID, currentUUID)
	}
}

func TestIntegrationClusterUnavailable(t *testing.T) {
	addr := os.Getenv("CLICKHOUSE_CLUSTER_OFFLINE_TEST_ADDR")
	if addr == "" {
		t.Skip("CLICKHOUSE_CLUSTER_OFFLINE_TEST_ADDR is not set")
	}
	conn := clusterDDLTestConnection(t, addr)
	name := fmt.Sprintf("terraform_offline_ddl_failure_%d", time.Now().UnixNano())
	cleanupClusterDDLTestTable(t, conn, name)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	data := clusterDDLTestData(t, name)
	client := &common.ApiClient{ClickhouseConnection: &conn}
	diags := resourceTableCreate(ctx, data, client)
	if !diags.HasError() {
		t.Errorf("create reported success while a cluster node was unavailable: %v", diags)
	}
	if !strings.Contains(fmt.Sprint(diags), "distributed_ddl_task_timeout") {
		t.Errorf("expected a distributed DDL timeout for the offline node, got %v", diags)
	}
	if data.Id() != "" {
		t.Errorf("incomplete cluster create retained resource ID %q", data.Id())
	}
}

func clusterDDLTestNodes(t *testing.T) []driver.Conn {
	t.Helper()
	raw := os.Getenv("CLICKHOUSE_CLUSTER_TEST_ADDRS")
	if raw == "" {
		t.Skip("CLICKHOUSE_CLUSTER_TEST_ADDRS is not set")
	}
	addrs := strings.Split(raw, ",")
	if len(addrs) != 2 {
		t.Fatal("CLICKHOUSE_CLUSTER_TEST_ADDRS must contain exactly two host:port endpoints")
	}
	return []driver.Conn{clusterDDLTestConnection(t, strings.TrimSpace(addrs[0])), clusterDDLTestConnection(t, strings.TrimSpace(addrs[1]))}
}

func TestIntegrationClusterRemoteAlterFailure(t *testing.T) {
	conns := clusterDDLTestNodes(t)
	name := fmt.Sprintf("terraform_remote_alter_failure_%d", time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	uuids := make([]string, len(conns))
	for i, conn := range conns {
		cleanupClusterDDLTestTable(t, conn, name)
		if err := conn.Exec(ctx, "CREATE TABLE default."+quoteIdent(name)+" (id UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
			t.Fatal(err)
		}
		if err := conn.Exec(ctx, fmt.Sprintf("INSERT INTO default.%s VALUES (%d)", quoteIdent(name), i+1)); err != nil {
			t.Fatal(err)
		}
		if err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = 'default' AND name = ?", name).Scan(&uuids[i]); err != nil {
			t.Fatal(err)
		}
	}
	if err := conns[1].Exec(ctx, "ALTER TABLE default."+quoteIdent(name)+" ADD COLUMN extra String"); err != nil {
		t.Fatal(err)
	}
	service := CHTableService{CHConnection: &conns[0]}
	table := TableResource{Database: "default", Name: name, Cluster: "test_cluster"}
	if err := service.executeAlterPlan(ctx, table, []string{"ADD COLUMN extra String"}); err == nil {
		t.Error("ALTER reported success despite a duplicate column on the remote node")
	}
	for i, conn := range conns {
		var uuid string
		if err := conn.QueryRow(ctx, "SELECT toString(uuid) FROM system.tables WHERE database = 'default' AND name = ?", name).Scan(&uuid); err != nil {
			t.Fatal(err)
		}
		var count, sum uint64
		if err := conn.QueryRow(ctx, "SELECT count(), sum(id) FROM default."+quoteIdent(name)).Scan(&count, &sum); err != nil {
			t.Fatal(err)
		}
		if uuid != uuids[i] || count != 1 || sum != uint64(i+1) {
			t.Errorf("node %d: failed ALTER changed UUID or data: uuid=%s, count=%d, sum=%d", i, uuid, count, sum)
		}
	}
}

func TestIntegrationClusterRemoteDropFailure(t *testing.T) {
	conns := clusterDDLTestNodes(t)
	name := fmt.Sprintf("terraform_remote_drop_failure_%d", time.Now().UnixNano())
	for _, conn := range conns {
		cleanupClusterDDLTestTable(t, conn, name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := conns[0].Exec(ctx, "CREATE TABLE default."+quoteIdent(name)+" (id UInt64) ENGINE = MergeTree ORDER BY id"); err != nil {
		t.Fatal(err)
	}
	data := clusterDDLTestData(t, name)
	id := "test_cluster:default:" + name
	data.SetId(id)
	client := &common.ApiClient{ClickhouseConnection: &conns[0]}
	diags := resourceTableDelete(ctx, data, client)
	if !diags.HasError() {
		t.Errorf("DROP reported success despite a missing table on the remote node: %v", diags)
	}
	if data.Id() != id {
		t.Errorf("failed cluster DROP changed resource ID from %q to %q", id, data.Id())
	}
}
