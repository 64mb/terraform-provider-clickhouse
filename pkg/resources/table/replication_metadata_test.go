package resourcetable

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type replicationMacroConn struct {
	driver.Conn
	overrides uint64
	err       error
	calls     int
}

func (c *replicationMacroConn) QueryRow(_ context.Context, query string, _ ...interface{}) driver.Row {
	c.calls++
	if !strings.Contains(query, "('database', 'table', 'uuid')") {
		panic("special macro guard must include all three overrides")
	}
	return replicationMacroRow{overrides: c.overrides, err: c.err}
}

type replicationMacroRow struct {
	driver.Row
	overrides uint64
	err       error
}

func (r replicationMacroRow) Scan(dest ...interface{}) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*uint64) = r.overrides
	return nil
}
func TestReplicationPathMacrosEnabled(t *testing.T) {
	for _, tc := range []struct {
		name, engine string
		overrides    uint64
		err          error
		enabled      bool
		calls        int
	}{
		{"builtins", "ReplicatedMergeTree", 0, nil, true, 1},
		{"variant", "ReplicatedReplacingMergeTree", 0, nil, true, 1},
		{"override disables all builtins", "ReplicatedMergeTree", 1, nil, false, 1},
		{"multiple overrides", "ReplicatedMergeTree", 3, nil, false, 1},
		{"metadata denied", "ReplicatedMergeTree", 0, errors.New("access denied"), false, 1},
		{"ordinary", "MergeTree", 0, nil, false, 0},
		{"distributed", "Distributed", 0, nil, false, 0},
		{"unknown replicated engine", "ReplicatedOther", 0, nil, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &replicationMacroConn{overrides: tc.overrides, err: tc.err}
			var conn driver.Conn = c
			if got := replicationPathMacrosEnabled(context.Background(), &conn, tc.engine); got != tc.enabled {
				t.Fatalf("enabled = %v, want %v", got, tc.enabled)
			}
			if c.calls != tc.calls {
				t.Fatalf("metadata calls = %d, want %d", c.calls, tc.calls)
			}
		})
	}
}
