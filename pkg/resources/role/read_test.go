package resourcerole

import (
	"context"
	"errors"
	"testing"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

type readTestRows struct {
	driver.Rows
	remaining bool
	closed    bool
	scanErr   error
	streamErr error
	closeErr  error
	scan      func(any)
	statement string
}

func (r *readTestRows) Next() bool   { next := r.remaining; r.remaining = false; return next }
func (r *readTestRows) Close() error { r.closed = true; return r.closeErr }
func (r *readTestRows) Err() error   { return r.streamErr }
func (r *readTestRows) ScanStruct(dest any) error {
	if r.scan != nil {
		r.scan(dest)
	}
	return r.scanErr
}
func (r *readTestRows) Scan(dest ...any) error { *dest[0].(*string) = r.statement; return r.scanErr }

type readTestConn struct {
	driver.Conn
	query func(string) (driver.Rows, error)
}

func (c *readTestConn) Query(_ context.Context, query string, _ ...any) (driver.Rows, error) {
	return c.query(query)
}

func readTestConnection(rows *readTestRows) driver.Conn {
	return &readTestConn{query: func(string) (driver.Rows, error) { return rows, nil }}
}

func TestGetRoleReleasesResultBeforeGrants(t *testing.T) {
	roleRows := &readTestRows{remaining: true}
	grants := &readTestRows{remaining: true, statement: "GRANT SELECT ON default.* TO reader"}
	calls := 0
	var conn driver.Conn = &readTestConn{query: func(string) (driver.Rows, error) {
		calls++
		if calls == 1 {
			return roleRows, nil
		}
		if !roleRows.closed {
			return nil, errors.New("single connection is still held by role lookup")
		}
		return grants, nil
	}}
	service := CHRoleService{CHConnection: &conn}
	role, err := service.GetRole(context.Background(), "reader")
	if err != nil {
		t.Fatal(err)
	}
	if role == nil || len(role.Privileges) != 1 {
		t.Fatalf("role = %#v", role)
	}
	if !roleRows.closed || !grants.closed {
		t.Error("role query results were not closed")
	}
}
func TestRoleGrantsResultLifetime(t *testing.T) {
	failure := errors.New("read failure")
	for _, tc := range []struct {
		name, statement              string
		present                      bool
		scanErr, streamErr, closeErr error
	}{
		{name: "found", present: true, statement: "GRANT SELECT ON default.* TO reader"},
		{name: "empty"}, {name: "scan error", present: true, scanErr: failure},
		{name: "parse error", present: true, statement: "invalid grant"}, {name: "stream error", streamErr: failure},
		{name: "late stream error", present: true, statement: "GRANT SELECT ON default.* TO reader", streamErr: failure},
		{name: "close error", present: true, statement: "GRANT SELECT ON default.* TO reader", closeErr: failure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows := &readTestRows{remaining: tc.present, statement: tc.statement, scanErr: tc.scanErr, streamErr: tc.streamErr, closeErr: tc.closeErr}
			conn := readTestConnection(rows)
			service := CHRoleService{CHConnection: &conn}
			_, err := service.getRoleGrants(context.Background(), "reader")
			if tc.scanErr != nil || tc.streamErr != nil || tc.closeErr != nil {
				if !errors.Is(err, failure) {
					t.Errorf("error = %v, want original failure", err)
				}
			} else if tc.name == "parse error" {
				if err == nil {
					t.Error("invalid grant accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if !rows.closed {
				t.Error("grant query result was not closed")
			}
		})
	}
}
func TestRoleRefreshMissingAndReadFailure(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "read failure"}[failed], func(t *testing.T) {
			rows := &readTestRows{}
			if failed {
				rows.streamErr = errors.New("interrupted read")
			}
			conn := readTestConnection(rows)
			data := schema.TestResourceDataRaw(t, ResourceRole().Schema, map[string]interface{}{"name": "reader", "database": "default"})
			data.SetId("reader")
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Errorf("refresh panicked: %v", recovered)
				}
			}()
			diags := resourceRoleRead(context.Background(), data, &common.ApiClient{ClickhouseConnection: &conn})
			if failed {
				if !diags.HasError() || data.Id() != "reader" {
					t.Errorf("failed read lost state or error: ID=%q, diagnostics=%v", data.Id(), diags)
				}
			} else if diags.HasError() || data.Id() != "" {
				t.Errorf("missing role retained state: ID=%q, diagnostics=%v", data.Id(), diags)
			}
		})
	}
}
