package resourcetable

import (
	"fmt"
	"strings"
)

// expands [Nested] columns into the arrays exposed by [system.columns]
func expandedColumns(cols []ColumnResource) ([]ColumnResource, error) {
	var out []ColumnResource
	seen := map[string]bool{}
	for _, c := range cols {
		typ, err := CanonicalType(c.Type)
		if err != nil {
			return nil, fmt.Errorf("column %q: %w", c.Name, err)
		}
		children := []ColumnResource{c}
		if strings.HasPrefix(typ, "Nested(") {
			if c.TTL != "" {
				return nil, fmt.Errorf("Nested column %q cannot have a TTL expression", c.Name)
			}
			if c.DefaultKind != "" || c.DefaultExpression != "" {
				return nil, fmt.Errorf("Nested column %q cannot have a default expression", c.Name)
			}
			p := typeParser{s: typ}
			p.ident()
			p.take('(')
			children = nil
			for {
				field, e := p.quoted('`')
				if e != nil {
					return nil, e
				}
				childType, e := p.typ(0)
				if e != nil {
					return nil, e
				}
				children = append(children, ColumnResource{Name: c.Name + "." + field, Type: "Array(" + childType + ")", Comment: c.Comment, Codec: c.Codec})
				if !p.take(',') {
					break
				}
			}
		}
		for _, child := range children {
			if seen[child.Name] {
				return nil, fmt.Errorf("duplicate expanded column %q", child.Name)
			}
			seen[child.Name] = true
			out = append(out, child)
		}
	}
	return out, nil
}

// restores configured [Nested] syntax only when every live child matches
func restoreNestedColumns(configured, actual []interface{}) []interface{} {
	result := append([]interface{}(nil), actual...)
	for _, raw := range configured {
		c, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name, _ := c["name"].(string)
		typ, _ := c["type"].(string)
		canonical, err := CanonicalType(typ)
		if err != nil || !strings.HasPrefix(canonical, "Nested(") {
			continue
		}
		expected, err := expandedColumns([]ColumnResource{{Name: name, Type: typ, DefaultKind: optionalColumnString(c, "default_kind"), DefaultExpression: optionalColumnString(c, "default_expression"), Codec: optionalColumnString(c, "codec"), TTL: optionalColumnString(c, "ttl"), Comment: optionalColumnString(c, "comment")}})
		if err != nil {
			continue
		}
		start := -1
		count := 0
		hasRoot := false
		for i, rawActual := range result {
			a, ok := rawActual.(map[string]interface{})
			if !ok {
				continue
			}
			n, _ := a["name"].(string)
			if n == name {
				hasRoot = true
			}
			if strings.HasPrefix(n, name+".") {
				if start < 0 {
					start = i
				}
				count++
			}
		}
		if hasRoot || start < 0 || count != len(expected) || start+count > len(result) {
			continue
		}
		matches := true
		for i, want := range expected {
			got, ok := result[start+i].(map[string]interface{})
			if !ok {
				matches = false
				break
			}
			gotType, e := CanonicalType(optionalColumnString(got, "type"))
			wantType, _ := CanonicalType(want.Type)
			if normalizeSQL(optionalColumnString(got, "codec")) != normalizeSQL(want.Codec) || normalizeSQL(optionalColumnString(got, "ttl")) != normalizeSQL(want.TTL) || e != nil || optionalColumnString(got, "name") != want.Name || gotType != wantType || optionalColumnString(got, "default_kind") != want.DefaultKind || normalizeSQL(optionalColumnString(got, "default_expression")) != normalizeSQL(want.DefaultExpression) || optionalColumnString(got, "comment") != want.Comment {
				matches = false
				break
			}
		}
		if matches {
			next := make([]interface{}, 0, len(result)-count+1)
			next = append(next, result[:start]...)
			next = append(next, raw)
			next = append(next, result[start+count:]...)
			result = next
		}
	}
	return result
}
