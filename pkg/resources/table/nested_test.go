package resourcetable

import (
	"reflect"
	"testing"
)

func TestExpandedColumns(t *testing.T) {
	cols := []ColumnResource{{Name: "id", Type: "UInt64"}, {Name: "events", Type: "Nested(`a,b` String, value Nullable(Int64))", Comment: "events", Codec: "ZSTD(1)"}}
	got, err := expandedColumns(cols)
	want := []ColumnResource{{Name: "id", Type: "UInt64"}, {Name: "events.a,b", Type: "Array(String)", Comment: "events", Codec: "ZSTD(1)"}, {Name: "events.value", Type: "Array(Nullable(Int64))", Comment: "events", Codec: "ZSTD(1)"}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, %v; want %#v", got, err, want)
	}
	for _, input := range [][]ColumnResource{
		{{Name: "n", Type: "Nested(a UInt8)", TTL: "now()"}},
		{{Name: "n", Type: "Nested(a UInt8)", DefaultKind: "DEFAULT", DefaultExpression: "[]"}},
		{{Name: "n", Type: "Nested(a UInt8)"}, {Name: "n.a", Type: "Array(UInt8)"}},
		{{Name: "n", Type: "Nested(a UInt8"}},
	} {
		if _, err := expandedColumns(input); err == nil {
			t.Fatalf("accepted invalid columns: %#v", input)
		}
	}
}

func TestRestoreNestedColumns(t *testing.T) {
	col := func(name, typ string) map[string]interface{} {
		return map[string]interface{}{"name": name, "type": typ, "comment": "", "default_kind": "", "default_expression": ""}
	}
	configured := []interface{}{col("id", "UInt64"), col("n", "Nested(a UInt8, b String)"), col("tail", "String")}
	live := func() []interface{} {
		return []interface{}{col("id", "UInt64"), col("n.a", "Array ( UInt8 )"), col("n.b", "Array(String)"), col("tail", "String")}
	}
	if got := restoreNestedColumns(configured, live()); !reflect.DeepEqual(got, configured) {
		t.Fatalf("did not restore nested syntax: %#v", got)
	}
	cases := map[string]func([]interface{}) []interface{}{
		"codec drift":   func(a []interface{}) []interface{} { a[1].(map[string]interface{})["codec"] = "ZSTD(1)"; return a },
		"ttl drift":     func(a []interface{}) []interface{} { a[1].(map[string]interface{})["ttl"] = "now()"; return a },
		"type drift":    func(a []interface{}) []interface{} { a[1].(map[string]interface{})["type"] = "Array(UInt16)"; return a },
		"comment drift": func(a []interface{}) []interface{} { a[1].(map[string]interface{})["comment"] = "changed"; return a },
		"default drift": func(a []interface{}) []interface{} {
			a[1].(map[string]interface{})["default_kind"] = "DEFAULT"
			a[1].(map[string]interface{})["default_expression"] = "[]"
			return a
		},
		"missing child":          func(a []interface{}) []interface{} { return append(a[:2], a[3:]...) },
		"extra child":            func(a []interface{}) []interface{} { return append(a, col("n.c", "Array(String)")) },
		"reordered children":     func(a []interface{}) []interface{} { a[1], a[2] = a[2], a[1]; return a },
		"noncontiguous children": func(a []interface{}) []interface{} { a[2], a[3] = a[3], a[2]; return a },
		"root collision":         func(a []interface{}) []interface{} { return append(a, col("n", "UInt8")) },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			actual := change(live())
			if got := restoreNestedColumns(configured, actual); !reflect.DeepEqual(got, actual) {
				t.Fatalf("hid live drift: %#v", got)
			}
		})
	}
}
