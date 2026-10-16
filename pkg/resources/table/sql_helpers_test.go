package resourcetable

import (
	"reflect"
	"strings"
	"testing"
)

func TestSQLLexicalHelpers(t *testing.T) {
	got, err := splitSQLList("coalesce(a, 'x,y'), tuple(1, [2, 3]), 'it\\'s,ok'")
	if err != nil || len(got) != 3 {
		t.Fatalf("split: %v %v", got, err)
	}
	for _, s := range []string{"x; DROP TABLE t", "x -- comment", "x /* comment */", "f(x]", "'unterminated"} {
		if validateSQLFragment(s) == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if normalizeSQL("f( 'a b', 2 )") != "f('a b',2)" {
		t.Fatal("normalization changed literal")
	}
	if quoteIdent("a`b") != "`a\\`b`" || quoteString("a'b\\c") != "'a\\'b\\\\c'" {
		t.Fatal("escaping")
	}
}
func TestTableMetadataRoundtrip(t *testing.T) {
	table := CHTable{Database: "db", Name: "t", Engine: "ReplicatedReplacingMergeTree", EngineFull: "ReplicatedReplacingMergeTree('/a,b', '{replica}', tuple(v, x)) PARTITION BY toYYYYMM(d) ORDER BY (x, v) SETTINGS index_granularity = 8192", SortingKey: "x, v", PrimaryKey: "x", PartitionKey: "toYYYYMM(d)", Comment: "plain's comment", CreateTableQuery: "CREATE TABLE t (x UInt64) ENGINE = MergeTree ORDER BY x TTL d + INTERVAL 1 DAY SETTINGS index_granularity = 8192, custom = 'a,b'"}
	got, err := table.ToResource()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.EngineParams, []string{"'/a,b'", "'{replica}'", "tuple(v, x)"}) || got.Comment != table.Comment || got.TTL != "d + INTERVAL 1 DAY" || got.Settings["custom"] != "'a,b'" {
		t.Fatalf("roundtrip: %#v", got)
	}
	table.Comment = `{"comment":"hello","cluster":"cluster"}`
	got, err = table.ToResource()
	if err != nil || got.Comment != "hello" || got.Cluster != "cluster" {
		t.Fatalf("legacy metadata: %#v %v", got, err)
	}
}
func TestCreateQuotesAndTupleKeys(t *testing.T) {
	r := TableResource{Database: "d-b", Name: "table", Engine: "MergeTree", Columns: []interface{}{map[string]interface{}{"name": "x", "type": "UInt64", "default_kind": "DEFAULT", "default_expression": "1", "comment": "it's x"}}, PartitionBy: []PartitionByResource{{By: "x"}, {By: "x + 1"}}, Settings: map[string]string{"z": "2", "a": "1"}, Comment: "it's table"}
	if err := validateTable(r); err != nil {
		t.Fatal(err)
	}
	sql := buildCreateOnClusterSentence(r)
	for _, part := range []string{"`d-b`.`table`", "`x` UInt64 DEFAULT 1 COMMENT 'it\\'s x'", "PARTITION BY (x, x + 1)", "ORDER BY tuple()", "SETTINGS `a` = 1, `z` = 2"} {
		if !strings.Contains(sql, part) {
			t.Errorf("missing %q in %s", part, sql)
		}
	}
}
