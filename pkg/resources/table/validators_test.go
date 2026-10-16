package resourcetable

import "testing"

func TestSchemaTypeDiagnostics(t *testing.T) {
	for _, typ := range []string{"LowCardinality(String)", "Nullable(Array(String))", "String); DROP TABLE t", ""} {
		diags := ValidateType(typ, nil)
		if got, want := diags.HasError(), typ != "LowCardinality(String)"; got != want {
			t.Fatalf("type %q diagnostics %v", typ, diags)
		}
	}
}
func TestEngineDiagnostics(t *testing.T) {
	for _, engine := range []string{"MergeTree", "ReplacingMergeTree", "SummingMergeTree", "AggregatingMergeTree", "CollapsingMergeTree", "VersionedCollapsingMergeTree", "GraphiteMergeTree"} {
		for _, prefix := range []string{"", "Replicated"} {
			if diags := ValidateOnClusterEngine(prefix+engine, nil); diags.HasError() {
				t.Fatalf("supported engine: %v", diags)
			}
		}
	}
	if diags := ValidateOnClusterEngine("Distributed", nil); diags.HasError() {
		t.Fatal(diags)
	}
	for _, engine := range []string{"", "Memory", "MergeTree; DROP TABLE t"} {
		if diags := ValidateOnClusterEngine(engine, nil); !diags.HasError() {
			t.Fatalf("accepted unsupported engine %q", engine)
		}
	}
}
func TestPartitionFunctionDiagnostics(t *testing.T) {
	for _, fn := range []string{"", "toYYYYMM", "toYYYYMMDD", "toYYYYMMDDhhmmss"} {
		if diags := ValidatePartitionBy(fn, nil); diags.HasError() {
			t.Fatal(diags)
		}
	}
	if diags := ValidatePartitionBy("arbitrary_function", nil); !diags.HasError() {
		t.Fatal("accepted a function outside the legacy partition API")
	}
}
