package resourcetable

import (
	"fmt"
	cty "github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"strings"
)

func ValidateType(v interface{}, p cty.Path) diag.Diagnostics { return ValidateDataType(v, p) }
func ValidatePartitionBy(v interface{}, p cty.Path) diag.Diagnostics {
	switch v.(string) {
	case "", "toYYYYMM", "toYYYYMMDD", "toYYYYMMDDhhmmss":
		return nil
	}
	return diag.Errorf("unsupported partition function %q; put an expression in by instead", v)
}
func mergeTreeEngine(e string) bool {
	e = strings.TrimPrefix(e, "Replicated")
	switch e {
	case "MergeTree", "ReplacingMergeTree", "SummingMergeTree", "AggregatingMergeTree", "CollapsingMergeTree", "VersionedCollapsingMergeTree", "GraphiteMergeTree":
		return true
	}
	return false
}
func ValidateOnClusterEngine(v interface{}, p cty.Path) diag.Diagnostics {
	if v.(string) == "Distributed" || mergeTreeEngine(v.(string)) {
		return nil
	}
	return diag.FromErr(fmt.Errorf("unsupported engine %q: use a MergeTree-family engine or Distributed", v))
}
