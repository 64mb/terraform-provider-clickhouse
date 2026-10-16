package resourcetable

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

func TestIntegrationMaintenanceRebuild(t *testing.T) {
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
	if err := conn.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	service := CHTableService{CHConnection: &conn}
	makeTable := func(suffix string) TableResource {
		table := TableResource{Database: "default", Name: fmt.Sprintf("tf_rebuild_%s_%d", suffix, time.Now().UnixNano()), Engine: "MergeTree", OrderBy: []string{"id"}, Columns: []interface{}{map[string]interface{}{"name": "id", "type": "UInt64"}, map[string]interface{}{"name": "label", "type": "String"}}}
		if err := service.CreateTable(ctx, table); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+qualified(table)) })
		return table
	}
	mustLive := func(table TableResource) *CHTable {
		live, err := service.GetTable(ctx, table.Database, table.Name)
		if err != nil {
			t.Fatal(err)
		}
		return live
	}
	cfg := func(operation string) map[string]interface{} {
		return map[string]interface{}{"enabled": true, "maintenance_acknowledged": true, "operation_id": operation}
	}
	cleanStage := func(table TableResource, operation string) {
		stage := table
		stage.Name = rebuildName(table, operation)
		t.Cleanup(func() { _ = conn.Exec(ctx, "DROP TABLE IF EXISTS "+qualified(stage)) })
	}
	t.Run("exchange and replay", func(t *testing.T) {
		table := makeTable("exchange")
		if err := conn.Exec(ctx, "INSERT INTO "+qualified(table)+" VALUES (1, 'a'), (2, 'b')"); err != nil {
			t.Fatal(err)
		}
		live := mustLive(table)
		next := table
		next.OrderBy = []string{"label", "id"}
		operation := "exchange"
		cleanStage(table, operation)
		backup, err := service.rebuildTable(ctx, live, next, cfg(operation), false)
		if err != nil {
			t.Fatal(err)
		}
		switched := mustLive(next)
		if switched.UUID == live.UUID {
			t.Fatal("source UUID did not change")
		}
		original, err := service.GetTable(ctx, table.Database, backup)
		if err != nil {
			t.Fatal(err)
		}
		if original.UUID != live.UUID {
			t.Fatalf("backup UUID %q, want %q", original.UUID, live.UUID)
		}
		for _, name := range []string{table.Name, backup} {
			var got string
			if err := conn.QueryRow(ctx, "SELECT groupArray(tuple(id,label))::String FROM (SELECT * FROM "+quoteIdent(table.Database)+"."+quoteIdent(name)+" ORDER BY id)").Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != "[(1,'a'),(2,'b')]" {
				t.Fatalf("rows in %s: %s", name, got)
			}
		}
		replay, err := service.rebuildTable(ctx, switched, next, cfg(operation), false)
		if err != nil {
			t.Fatal(err)
		}
		if replay != backup || mustLive(next).UUID != switched.UUID {
			t.Fatal("replay exchanged tables again")
		}
		other := next
		other.OrderBy = []string{"id"}
		if _, err := service.rebuildTable(ctx, switched, other, cfg(operation), false); err == nil || !strings.Contains(err.Error(), "different target") {
			t.Fatalf("reused operation: %v", err)
		}
	})
	t.Run("partial staging retry", func(t *testing.T) {
		table := makeTable("retry")
		if err := conn.Exec(ctx, "INSERT INTO "+qualified(table)+" VALUES (1,'oops'),(2,'2')"); err != nil {
			t.Fatal(err)
		}
		live := mustLive(table)
		next := table
		next.Columns = []interface{}{map[string]interface{}{"name": "id", "type": "UInt64"}, map[string]interface{}{"name": "label", "type": "UInt64"}}
		operation := "retry"
		cleanStage(table, operation)
		if _, err := service.rebuildTable(ctx, live, next, cfg(operation), false); err == nil {
			t.Fatal("invalid cast succeeded")
		}
		if mustLive(table).UUID != live.UUID {
			t.Fatal("failed rebuild replaced source")
		}
		stage := next
		stage.Name = rebuildName(next, operation)
		if err := conn.Exec(ctx, "INSERT INTO "+qualified(stage)+" VALUES (99,99)"); err != nil {
			t.Fatal(err)
		}
		if err := conn.Exec(ctx, "ALTER TABLE "+qualified(table)+" UPDATE label = '123' WHERE id = 1 SETTINGS mutations_sync = 2"); err != nil {
			t.Fatal(err)
		}
		if _, err := service.rebuildTable(ctx, mustLive(table), next, cfg(operation), false); err != nil {
			t.Fatal(err)
		}
		var count, sum uint64
		if err := conn.QueryRow(ctx, "SELECT count(),sum(label) FROM "+qualified(table)).Scan(&count, &sum); err != nil {
			t.Fatal(err)
		}
		if count != 2 || sum != 125 {
			t.Fatalf("retry retained partial copy: count=%d sum=%d", count, sum)
		}
	})
	t.Run("tampered staging rejected", func(t *testing.T) {
		table := makeTable("tamper")
		if err := conn.Exec(ctx, "INSERT INTO "+qualified(table)+" VALUES (1,'oops')"); err != nil {
			t.Fatal(err)
		}
		live := mustLive(table)
		next := table
		next.Columns = []interface{}{map[string]interface{}{"name": "id", "type": "UInt64"}, map[string]interface{}{"name": "label", "type": "UInt64"}}
		operation := "tamper"
		cleanStage(table, operation)
		if _, err := service.rebuildTable(ctx, live, next, cfg(operation), false); err == nil {
			t.Fatal("invalid cast succeeded")
		}
		stage := next
		stage.Name = rebuildName(next, operation)
		if err := conn.Exec(ctx, "ALTER TABLE "+qualified(stage)+" ADD COLUMN unexpected UInt64"); err != nil {
			t.Fatal(err)
		}
		if err := conn.Exec(ctx, "INSERT INTO "+qualified(stage)+" VALUES (99,99,99)"); err != nil {
			t.Fatal(err)
		}
		if _, err := service.rebuildTable(ctx, mustLive(table), next, cfg(operation), false); err == nil {
			t.Fatal("tampered staging accepted")
		}
		var count uint64
		if err := conn.QueryRow(ctx, "SELECT count() FROM "+qualified(stage)+" WHERE id=99").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatal("tampered stage was truncated")
		}
		if mustLive(table).UUID != live.UUID {
			t.Fatal("tampered retry exchanged source")
		}
	})
	for _, change := range []struct{ name, ddl string }{
		{"ttl", "MODIFY TTL toDateTime(id) + INTERVAL 100 YEAR"},
		{"index", "ADD INDEX tampered label TYPE minmax GRANULARITY 1"},
		{"projection", "ADD PROJECTION tampered (SELECT id, label ORDER BY label)"},
	} {
		t.Run("tampered staging "+change.name, func(t *testing.T) {
			table := makeTable("metadata")
			if err := conn.Exec(ctx, "INSERT INTO "+qualified(table)+" VALUES (1,'oops')"); err != nil {
				t.Fatal(err)
			}
			live := mustLive(table)
			next := table
			next.Columns = []interface{}{map[string]interface{}{"name": "id", "type": "UInt64"}, map[string]interface{}{"name": "label", "type": "UInt64"}}
			operation := "metadata"
			cleanStage(table, operation)
			if _, err := service.rebuildTable(ctx, live, next, cfg(operation), false); err == nil {
				t.Fatal("invalid cast succeeded")
			}
			stage := next
			stage.Name = rebuildName(next, operation)
			if err := conn.Exec(ctx, "INSERT INTO "+qualified(stage)+" VALUES (99,99)"); err != nil {
				t.Fatal(err)
			}
			if err := conn.Exec(ctx, "ALTER TABLE "+qualified(stage)+" "+change.ddl); err != nil {
				t.Fatal(err)
			}
			if _, err := service.rebuildTable(ctx, mustLive(table), next, cfg(operation), false); err == nil {
				t.Fatal("tampered stage metadata accepted")
			}
			var count uint64
			if err := conn.QueryRow(ctx, "SELECT count() FROM "+qualified(stage)+" WHERE id=99").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatal("tampered stage sentinel was removed")
			}
			if mustLive(table).UUID != live.UUID {
				t.Fatal("tampered stage exchanged source")
			}
		})
	}
	t.Run("completed live tamper rejected", func(t *testing.T) {
		table := makeTable("completed")
		if err := conn.Exec(ctx, "INSERT INTO "+qualified(table)+" VALUES (1,'a'),(2,'b')"); err != nil {
			t.Fatal(err)
		}
		source := mustLive(table)
		next := table
		next.OrderBy = []string{"label", "id"}
		operation := "completed"
		cleanStage(table, operation)
		backup, err := service.rebuildTable(ctx, source, next, cfg(operation), false)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Exec(ctx, "ALTER TABLE "+qualified(table)+" ADD COLUMN unexpected UInt64"); err != nil {
			t.Fatal(err)
		}
		if _, err := service.rebuildTable(ctx, mustLive(table), next, cfg(operation), false); err == nil {
			t.Fatal("completed tampered definition accepted")
		}
		retained, err := service.GetTable(ctx, table.Database, backup)
		if err != nil {
			t.Fatal(err)
		}
		if retained.UUID != source.UUID {
			t.Fatal("retained backup UUID changed")
		}
		var count, sum uint64
		if err := conn.QueryRow(ctx, "SELECT count(),sum(id) FROM "+quoteIdent(table.Database)+"."+quoteIdent(backup)).Scan(&count, &sum); err != nil {
			t.Fatal(err)
		}
		if count != 2 || sum != 3 {
			t.Fatal("retained backup rows changed")
		}
	})
	t.Run("preflight rejection", func(t *testing.T) {
		table := makeTable("guard")
		live := mustLive(table)
		denied := cfg("denied")
		denied["maintenance_acknowledged"] = false
		if _, err := service.rebuildTable(ctx, live, table, denied, false); err == nil {
			t.Fatal("maintenance acknowledgement was ignored")
		}
		for _, engine := range []string{"ReplicatedMergeTree", "Distributed"} {
			next := table
			next.Engine = engine
			if err := service.rebuildPreflight(ctx, live, next); err == nil {
				t.Fatalf("accepted %s", engine)
			}
		}
		ttl := table
		ttl.TTL = "now() + INTERVAL 1 DAY"
		if err := service.rebuildPreflight(ctx, live, ttl); err == nil {
			t.Fatal("accepted TTL")
		}
		view := table
		view.Name += "_view"
		if err := conn.Exec(ctx, "CREATE VIEW "+qualified(view)+" AS SELECT * FROM "+qualified(table)); err != nil {
			t.Fatal(err)
		}
		defer conn.Exec(ctx, "DROP VIEW "+qualified(view))
		if err := service.rebuildPreflight(ctx, live, table); err == nil {
			t.Fatal("accepted dependent view")
		}
		if mustLive(table).UUID != live.UUID {
			t.Fatal("preflight changed source")
		}
	})
}

// [exchangeFaultConn] simulates a lost acknowledgement or an exchange rejected before execution
type exchangeFaultConn struct {
	driver.Conn
	after     bool
	exchanges int
}

func (c *exchangeFaultConn) Exec(ctx context.Context, query string, args ...interface{}) error {
	if strings.HasPrefix(query, "EXCHANGE TABLES") {
		c.exchanges++
		if c.exchanges == 1 {
			if c.after {
				if err := c.Conn.Exec(ctx, query, args...); err != nil {
					return err
				}
			}
			return fmt.Errorf("injected exchange transport failure")
		}
	}
	return c.Conn.Exec(ctx, query, args...)
}
func TestIntegrationRebuildExchangeRecovery(t *testing.T) {
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
	base := CHTableService{CHConnection: &conn}
	for _, after := range []bool{false, true} {
		t.Run(fmt.Sprintf("lost_ack_%t", after), func(t *testing.T) {
			table := TableResource{Database: "default", Name: fmt.Sprintf("tf_fault_%d", time.Now().UnixNano()), Engine: "MergeTree", OrderBy: []string{"id"}, Columns: []interface{}{map[string]interface{}{"name": "id", "type": "UInt64"}, map[string]interface{}{"name": "label", "type": "String"}}}
			if err := base.CreateTable(ctx, table); err != nil {
				t.Fatal(err)
			}
			defer base.DeleteTable(ctx, table)
			if err := conn.Exec(ctx, "INSERT INTO "+qualified(table)+" VALUES (1,'a'),(2,'b')"); err != nil {
				t.Fatal(err)
			}
			next := table
			next.OrderBy = []string{"label", "id"}
			stage := table
			stage.Name = rebuildName(next, "fault")
			defer base.DeleteTable(ctx, stage)
			cfg := map[string]interface{}{"enabled": true, "maintenance_acknowledged": true, "operation_id": "fault"}
			live, err := base.GetTable(ctx, table.Database, table.Name)
			if err != nil {
				t.Fatal(err)
			}
			sourceUUID := live.UUID
			wrapped := &exchangeFaultConn{Conn: conn, after: after}
			var faulty driver.Conn = wrapped
			service := CHTableService{CHConnection: &faulty}
			if _, err := service.rebuildTable(ctx, live, next, cfg, false); err == nil {
				t.Fatal("exchange fault not returned")
			}
			live, err = base.GetTable(ctx, table.Database, table.Name)
			if err != nil {
				t.Fatal(err)
			}
			if (live.UUID != sourceUUID) != after {
				t.Fatal("unexpected switch before retry")
			}
			backup, err := service.rebuildTable(ctx, live, next, cfg, false)
			if err != nil {
				t.Fatal(err)
			}
			expected := 2
			if after {
				expected = 1
			}
			if wrapped.exchanges != expected {
				t.Fatalf("exchanges %d, want %d", wrapped.exchanges, expected)
			}
			retained, err := base.GetTable(ctx, table.Database, backup)
			if err != nil {
				t.Fatal(err)
			}
			if retained.UUID != sourceUUID {
				t.Fatal("backup UUID changed")
			}
			for _, name := range []string{table.Name, backup} {
				var count, sum uint64
				if err := conn.QueryRow(ctx, "SELECT count(),sum(id) FROM "+quoteIdent(table.Database)+"."+quoteIdent(name)).Scan(&count, &sum); err != nil {
					t.Fatal(err)
				}
				if count != 2 || sum != 3 {
					t.Fatal("recovery lost or duplicated rows")
				}
			}
		})
	}
}
