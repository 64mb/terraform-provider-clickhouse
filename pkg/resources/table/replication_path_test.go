package resourcetable

import "testing"

func TestEquivalentReplicationPath(t *testing.T) {
	cases := []struct {
		name, template, actual, database, table string
		want                                    bool
	}{
		{"both", "'/a/{database}/{table}/{shard}'", "'/a/db/t/{shard}'", "db", "t", true},
		{"repeated", "'{table}/{database}/{table}/{database}'", "'t/db/t/db'", "db", "t", true},
		{"no recursive substitution", "'{database}/{table}'", "'{table}/t'", "{table}", "t", true},
		{"case matters", "'{database}'", "'DB'", "db", "t", false},
		{"other macros remain literal", "'{database}/{uuid}/{replica}/{unknown}'", "'db/{uuid}/{replica}/{unknown}'", "db", "t", true},
		{"other macros not expanded", "'{shard}'", "'01'", "db", "t", false},
		{"unicode", "'/{database}/{table}'", "'/база/表'", "база", "表", true},
		{"equivalent quoting", `'/it\'s/{database}'`, `'/it''s/db'`, "db", "t", true},
		{"escaped identifiers", "'{database}/{table}'", `'d\'b/t\\x'`, "d'b", `t\x`, true},
		{"hex on both sides", `'\x7bdatabase\x7d'`, `'\x64b'`, "db", "t", true},
		{"uppercase hex", `'\xDB'`, `'\xdb'`, "db", "t", true},
		{"controls", `'\a\b\e\f\n\r\t\v\0'`, "'\a\b\x1b\f\n\r\t\v\x00'", "db", "t", true},
		{"empty", "''", "''", "db", "t", true},
		{"outer whitespace", "  '{database}'\n", " 'db' ", "db", "t", true},
		{"nested macro is not a builtin", "'{custom{database}}'", "'{customdb}'", "db", "t", false},
		{"unbalanced macro", "'/{database'", "'/db'", "db", "t", false},
		{"content whitespace preserved", "' {database} '", "'db'", "db", "t", false},
		{"content whitespace equal", "' {database} '", "' db '", "db", "t", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := equivalentReplicationPath(tc.template, tc.actual, tc.database, tc.table); got != tc.want {
				t.Fatalf("equivalentReplicationPath(%q, %q) = %v, want %v", tc.template, tc.actual, got, tc.want)
			}
		})
	}
}

func TestEquivalentReplicationPathRejectsNonLiterals(t *testing.T) {
	for _, invalid := range []string{
		"", "db", `"db"`, "`db`", "'db", "db'", "'d'b'", `'db\'`,
		"'db' || ''", "concat('db', '')", "'db'; SELECT 1", "'db' -- comment", "'db'/*comment*/", "'db' ''",
		`'\q'`, `'\u0064b'`, `'\x'`, `'\x6'`, `'\xGG'`,
	} {
		t.Run(invalid, func(t *testing.T) {
			if equivalentReplicationPath(invalid, "'db'", "db", "t") || equivalentReplicationPath("'db'", invalid, "db", "t") {
				t.Fatalf("accepted nonliteral or unsupported escape %q", invalid)
			}
		})
	}
}
