package resourcetable

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestReplicationPathDiff(t *testing.T) {
	cases := []struct {
		name, engine, key, old, next string
		enabled, equal               bool
	}{
		{"database", "ReplicatedMergeTree", "engine_params.0", "'/tables/analytics/events/{shard}'", "'/tables/{database}/events/{shard}'", true, true},
		{"table", "ReplicatedReplacingMergeTree", "engine_params.0", "'/tables/analytics/events/{shard}'", "'/tables/analytics/{table}/{shard}'", true, true},
		{"both", "ReplicatedMergeTree", "engine_params.0", "'/tables/analytics/events/{shard}'", "'/tables/{database}/{table}/{shard}'", true, true},
		{"explicit path replaces template", "ReplicatedMergeTree", "engine_params.0", "'/tables/{database}/{table}/{shard}'", "'/tables/analytics/events/{shard}'", true, true},
		{"real path change", "ReplicatedMergeTree", "engine_params.0", "'/tables/other/events/{shard}'", "'/tables/{database}/{table}/{shard}'", true, false},
		{"shard change", "ReplicatedMergeTree", "engine_params.0", "'/tables/analytics/events/01'", "'/tables/{database}/{table}/{shard}'", true, false},
		{"uuid remains literal", "ReplicatedMergeTree", "engine_params.0", "'/tables/analytics/events/{uuid}'", "'/tables/{database}/{table}/{uuid}'", true, true},
		{"uuid change", "ReplicatedMergeTree", "engine_params.0", "'/tables/analytics/events/uuid'", "'/tables/{database}/{table}/{uuid}'", true, false},
		{"disabled special macros", "ReplicatedMergeTree", "engine_params.0", "'/tables/analytics/events/{shard}'", "'/tables/{database}/{table}/{shard}'", false, false},
		{"replica argument", "ReplicatedMergeTree", "engine_params.1", "'analytics'", "'{database}'", true, false},
		{"version argument", "ReplicatedReplacingMergeTree", "engine_params.2", "'events'", "'{table}'", true, false},
		{"distributed engine", "Distributed", "engine_params.0", "'analytics'", "'{database}'", true, false},
		{"ordinary engine", "MergeTree", "engine_params.0", "'analytics'", "'{database}'", true, false},
		{"other field", "ReplicatedMergeTree", "comment", "'analytics'", "'{database}'", true, false},
		{"literal case significant", "ReplicatedMergeTree", "engine_params.0", "'/tables/Analytics/events'", "'/tables/{database}/{table}'", true, false},
		{"expression is not literal", "ReplicatedMergeTree", "engine_params.0", "'/tables/analytics'", "concat('/tables/', '{database}')", true, false},
		{"no normalization of expressions", "ReplicatedMergeTree", "engine_params.0", "'/tables/analytics'", "'/tables/' || '{database}'", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := ResourceTable()
			d := schema.TestResourceDataRaw(t, r.Schema, map[string]interface{}{
				"database": "analytics", "name": "events", "engine": tc.engine,
				"replication_path_macros_enabled": tc.enabled,
			})
			if got := r.Schema["engine_params"].DiffSuppressFunc(tc.key, tc.old, tc.next, d); got != tc.equal {
				t.Fatalf("equivalence = %v, want %v", got, tc.equal)
			}
		})
	}
}
