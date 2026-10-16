package resourcetable

import (
	"reflect"
	"strings"
	"testing"
)

func plannerTable(columns ...ColumnResource) TableResource {
	r := TableResource{Database: "db", Name: "events", Engine: "MergeTree", OrderBy: []string{"id"}, PrimaryKey: []string{"id"}}
	for _, c := range columns {
		r.Columns = append(r.Columns, map[string]interface{}{"name": c.Name, "type": c.Type, "default_kind": c.DefaultKind, "default_expression": c.DefaultExpression, "comment": c.Comment})
	}
	return r
}
func TestAlterPlanAddsSortingColumnInSinglePlan(t *testing.T) {
	old := plannerTable(ColumnResource{Name: "id", Type: "UInt64"})
	next := plannerTable(ColumnResource{Name: "id", Type: "UInt64"}, ColumnResource{Name: "created", Type: "DateTime"})
	next.OrderBy = []string{"id", "created"}
	got, err := buildAlterPlan(old, next, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"ADD COLUMN `created` DateTime AFTER `id`", "MODIFY ORDER BY (id, created)"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("actions = %#v, want %#v", got, want)
	}
}
func TestAlterPlanRejectsUnsafeSortChanges(t *testing.T) {
	old := plannerTable(ColumnResource{Name: "id", Type: "UInt64"}, ColumnResource{Name: "existing", Type: "UInt64"})
	cases := []struct {
		name  string
		keys  []string
		extra ColumnResource
	}{
		{name: "remove old key", keys: nil},
		{name: "replace old key", keys: []string{"existing"}},
		{name: "append existing column", keys: []string{"id", "existing"}},
		{name: "append expression", keys: []string{"id", "toDate(created)"}, extra: ColumnResource{Name: "created", Type: "DateTime"}},
		{name: "append default column", keys: []string{"id", "created"}, extra: ColumnResource{Name: "created", Type: "DateTime", DefaultKind: "DEFAULT", DefaultExpression: "now()"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := old
			next.OrderBy = tc.keys
			next.Columns = append([]interface{}{}, old.Columns...)
			if tc.extra.Name != "" {
				c := tc.extra
				next.Columns = append(next.Columns, map[string]interface{}{"name": c.Name, "type": c.Type, "default_kind": c.DefaultKind, "default_expression": c.DefaultExpression})
			}
			actions, err := buildAlterPlan(old, next, true)
			if err == nil || actions != nil {
				t.Fatalf("unsafe plan: %#v, %v", actions, err)
			}
		})
	}
}
func TestAlterPlanRequiresExplicitColumnDrop(t *testing.T) {
	old := plannerTable(ColumnResource{Name: "id", Type: "UInt64"}, ColumnResource{Name: "payload", Type: "String"})
	next := plannerTable(ColumnResource{Name: "id", Type: "UInt64"})
	if actions, err := buildAlterPlan(old, next, false); err == nil || actions != nil {
		t.Fatalf("unapproved drop: %#v %v", actions, err)
	}
	actions, err := buildAlterPlan(old, next, true)
	if err != nil || !reflect.DeepEqual(actions, []string{"DROP COLUMN `payload`"}) {
		t.Fatalf("approved drop: %#v %v", actions, err)
	}
}
func TestAlterPlanRemovesDefaultBeforeModifyingColumn(t *testing.T) {
	old := plannerTable(ColumnResource{Name: "id", Type: "UInt64", DefaultKind: "DEFAULT", DefaultExpression: "1"})
	next := plannerTable(ColumnResource{Name: "id", Type: "UInt64"})
	actions, err := buildAlterPlan(old, next, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"MODIFY COLUMN `id` REMOVE DEFAULT", "MODIFY COLUMN `id` UInt64 FIRST"}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions = %#v, want %#v", actions, want)
	}
}
func TestAlterPlanSettingsAreSortedAndRemovedSettingsReset(t *testing.T) {
	old := plannerTable(ColumnResource{Name: "id", Type: "UInt64"})
	old.Settings = map[string]string{"removed_z": "1", "removed_a": "2", "unchanged": "'a b'", "modified": "1"}
	next := plannerTable(ColumnResource{Name: "id", Type: "UInt64"})
	next.Settings = map[string]string{"added_z": "2", "added_a": "1", "modified": "3", "unchanged": "'a b'"}
	actions, err := buildAlterPlan(old, next, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"MODIFY SETTING `added_a` = 1", "MODIFY SETTING `added_z` = 2", "MODIFY SETTING `modified` = 3", "RESET SETTING `removed_a`", "RESET SETTING `removed_z`"}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("actions = %#v, want %#v", actions, want)
	}
}
func TestImmutableChangesRejectAlter(t *testing.T) {
	old := plannerTable(ColumnResource{Name: "id", Type: "UInt64"})
	for _, tc := range []struct {
		name   string
		change func(*TableResource)
	}{
		{"engine", func(r *TableResource) { r.Engine = "ReplacingMergeTree" }},
		{"engine arguments", func(r *TableResource) { r.EngineParams = []string{"id"} }},
		{"partition", func(r *TableResource) { r.PartitionBy = []PartitionByResource{{By: "id"}} }},
		{"primary key", func(r *TableResource) { r.PrimaryKey = []string{"id + 1"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next := old
			tc.change(&next)
			if !immutableChange(old, next) {
				t.Fatal("change classified as mutable")
			}
			actions, err := buildAlterPlan(old, next, true)
			if err == nil || actions != nil || !strings.Contains(err.Error(), "maintenance rebuild") {
				t.Fatalf("actions = %#v, err = %v", actions, err)
			}
		})
	}
	if immutableChange(old, old) {
		t.Fatal("unchanged table classified as immutable change")
	}
}
func TestSameExpressionsPreservesOrderAndLiteralContent(t *testing.T) {
	for _, tc := range []struct {
		a, b []string
		want bool
	}{
		{[]string{"toDate( created )", "`id`"}, []string{"toDate(created)", "id"}, true},
		{[]string{"id", "created"}, []string{"created", "id"}, false},
		{[]string{"f('a b')"}, []string{"f('ab')"}, false},
		{[]string{"a b"}, []string{"ab"}, false},
		{nil, []string{}, true},
	} {
		if got := sameExpressions(tc.a, tc.b); got != tc.want {
			t.Errorf("sameExpressions(%q,%q) = %v", tc.a, tc.b, got)
		}
	}
}
