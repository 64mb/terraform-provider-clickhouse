package resourcetable

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func sameExpressions(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if normalizeSQL(a[i]) != normalizeSQL(b[i]) {
			return false
		}
	}
	return true
}
func immutableChange(a, b TableResource) bool {
	return normalizeSQL(a.SampleBy) != normalizeSQL(b.SampleBy) || a.Engine != b.Engine || !sameExpressions(a.EngineParams, b.EngineParams) || normalizeSQL(buildPartitionBySentence(a.PartitionBy)) != normalizeSQL(buildPartitionBySentence(b.PartitionBy)) || !sameExpressions(a.PrimaryKey, b.PrimaryKey)
}

// validate every planned action before any DDL is executed
func buildAlterPlan(old, next TableResource, allowDrop bool) ([]string, error) {
	if immutableChange(old, next) {
		return nil, fmt.Errorf("engine, engine arguments, partition or primary key changes require an explicitly enabled maintenance rebuild")
	}
	if v, ok := next.Settings["index_granularity"]; ok {
		if normalizeSQL(v) != normalizeSQL(old.Settings["index_granularity"]) {
			return nil, fmt.Errorf("index_granularity is immutable in ClickHouse 25.3; use a maintenance rebuild")
		}
	} else if _, ok := old.Settings["index_granularity"]; ok {
		return nil, fmt.Errorf("resetting index_granularity requires a maintenance rebuild")
	}

	oldCols, e := expandedColumns(old.GetColumnsResourceList())
	if e != nil {
		return nil, e
	}
	newCols, e := expandedColumns(next.GetColumnsResourceList())
	if e != nil {
		return nil, e
	}
	byName := map[string]ColumnResource{}
	newNames := map[string]bool{}
	for _, c := range oldCols {
		byName[c.Name] = c
	}
	for _, c := range newCols {
		newNames[c.Name] = true
	}
	actions := []string{}
	for _, c := range oldCols {
		if !newNames[c.Name] {
			if !allowDrop {
				return nil, fmt.Errorf("dropping column %q requires allow_column_drop = true; renames must be performed explicitly outside this resource", c.Name)
			}
			actions = append(actions, "DROP COLUMN "+quoteIdent(c.Name))
		}
	}
	for i, c := range newCols {
		prev, exists := byName[c.Name]
		position := " FIRST"
		if i > 0 {
			position = " AFTER " + quoteIdent(newCols[i-1].Name)
		}
		declaration := buildColumnsSentence([]ColumnResource{c})[0]
		if !exists {
			actions = append(actions, "ADD COLUMN "+declaration+position)
			continue
		}
		if prev.Codec != "" && c.Codec == "" {
			actions = append(actions, "MODIFY COLUMN "+quoteIdent(c.Name)+" REMOVE CODEC")
		}
		if prev.TTL != "" && c.TTL == "" {
			actions = append(actions, "MODIFY COLUMN "+quoteIdent(c.Name)+" REMOVE TTL")
		}
		if prev.DefaultKind != "" && c.DefaultKind == "" {
			actions = append(actions, "MODIFY COLUMN "+quoteIdent(c.Name)+" REMOVE "+prev.DefaultKind)
		}
		// positions are explicit so refresh preserves the configured column order
		changed := !reflect.DeepEqual(prev, c) || i >= len(oldCols) || oldCols[i].Name != c.Name
		if changed {
			actions = append(actions, "MODIFY COLUMN "+declaration+position)
		}
		if prev.Comment != c.Comment && c.Comment == "" {
			actions = append(actions, "COMMENT COLUMN "+quoteIdent(c.Name)+" ''")
		}
	}
	if !sameExpressions(old.OrderBy, next.OrderBy) {
		// new sorting expressions may only append newly added columns without defaults
		if len(next.OrderBy) < len(old.OrderBy) || !sameExpressions(old.OrderBy, next.OrderBy[:len(old.OrderBy)]) {
			return nil, fmt.Errorf("this ORDER BY change requires a maintenance rebuild")
		}
		for _, expr := range next.OrderBy[len(old.OrderBy):] {
			name := strings.Trim(expr, "`\"")
			if _, ok := byName[name]; ok {
				return nil, fmt.Errorf("ORDER BY expressions using existing columns require a maintenance rebuild")
			}
			found := false
			for _, c := range newCols {
				if c.Name == name && c.DefaultKind == "" {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("ALTER ORDER BY only supports appending plain newly added columns without defaults; otherwise enable a maintenance rebuild")
			}
		}
		actions = append(actions, "MODIFY ORDER BY ("+strings.Join(next.OrderBy, ", ")+")")
	}
	if old.Comment != next.Comment {
		actions = append(actions, "MODIFY COMMENT "+quoteString(storedComment(next.Comment, next.Cluster)))
	}
	if normalizeSQL(old.TTL) != normalizeSQL(next.TTL) {
		if next.TTL == "" {
			actions = append(actions, "REMOVE TTL")
		} else {
			actions = append(actions, "MODIFY TTL "+next.TTL)
		}
	}
	keys := []string{}
	for k := range next.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if normalizeSQL(old.Settings[k]) != normalizeSQL(next.Settings[k]) {
			actions = append(actions, "MODIFY SETTING "+quoteIdent(k)+" = "+next.Settings[k])
		}
	}
	keys = nil
	for k := range old.Settings {
		if _, ok := next.Settings[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		actions = append(actions, "RESET SETTING "+quoteIdent(k))
	}
	if len(actions) > 0 && !mergeTreeEngine(old.Engine) {
		return nil, fmt.Errorf("ALTER is supported only for MergeTree-family tables; Distributed definitions require an external topology migration")
	}
	return actions, nil
}
func resourceTableUpdate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	for _, k := range []string{"database", "name", "cluster"} {
		if d.HasChange(k) {
			return diag.Errorf("changing %s is not supported in place; import a separately migrated table instead", k)
		}
	}
	client := meta.(*common.ApiClient)
	s := CHTableService{CHConnection: client.ClickhouseConnection}
	next := tableFromData(d)
	if err := validateTable(next); err != nil {
		return diag.FromErr(err)
	}
	live, err := s.GetTable(ctx, next.Database, next.Name)
	if err != nil {
		return diag.FromErr(err)
	}
	old, err := live.ToResource()
	if err != nil {
		return diag.FromErr(err)
	}
	omitImplicitDefaults(next, old)
	preserveConfiguredExpressions(ctx, client.ClickhouseConnection, d, old)
	old.Cluster = next.Cluster
	old.Columns = restoreNestedColumns(next.Columns, old.Columns)
	if len(next.PrimaryKey) == 0 {
		// the original primary key is retained when the sorting key is altered
		next.PrimaryKey = old.PrimaryKey
	}
	cfg := d.Get("rebuild").([]interface{})
	requested := tableFromData(d)
	if !hasConfiguredPrimaryKey(d) {
		requested.PrimaryKey = nil
	}
	if len(cfg) > 0 && cfg[0].(map[string]interface{})["enabled"].(bool) {
		if backup, done, e := s.completedRebuild(ctx, live, requested, cfg[0].(map[string]interface{})); done {
			if e != nil {
				return diag.FromErr(e)
			}
			if e = d.Set("rebuild_backup_table", backup); e != nil {
				return diag.FromErr(e)
			}
			return resourceTableRead(ctx, d, meta)
		}
	}
	actions, planErr := buildAlterPlan(*old, next, d.Get("allow_column_drop").(bool))
	if planErr != nil {
		if len(cfg) == 0 || !cfg[0].(map[string]interface{})["enabled"].(bool) {
			return diag.FromErr(planErr)
		}
		backup, err := s.rebuildTable(ctx, live, requested, cfg[0].(map[string]interface{}), d.Get("allow_column_drop").(bool))
		if err != nil {
			return diag.FromErr(err)
		}
		if err := d.Set("rebuild_backup_table", backup); err != nil {
			return diag.FromErr(err)
		}
	} else if len(actions) > 0 {
		if err := s.executeAlterPlan(ctx, next, actions); err != nil {
			return diag.FromErr(err)
		}
	}
	return resourceTableRead(ctx, d, meta)
}

// column changes and a sorting-key extension share one ALTER, other commands have standalone grammar
func (s *CHTableService) executeAlterPlan(ctx context.Context, t TableResource, actions []string) error {
	ctx = clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{"mutations_sync": 2, "alter_sync": 2}))
	var columns []string
	var standalone []string
	var removals []string
	for _, a := range actions {
		if strings.HasPrefix(a, "MODIFY COLUMN ") && (strings.HasSuffix(a, " REMOVE DEFAULT") || strings.HasSuffix(a, " REMOVE MATERIALIZED") || strings.HasSuffix(a, " REMOVE ALIAS") || strings.HasSuffix(a, " REMOVE CODEC") || strings.HasSuffix(a, " REMOVE TTL")) {
			removals = append(removals, a)
			continue
		}
		if strings.HasPrefix(a, "ADD COLUMN ") || strings.HasPrefix(a, "DROP COLUMN ") || strings.HasPrefix(a, "MODIFY COLUMN ") || strings.HasPrefix(a, "COMMENT COLUMN ") || strings.HasPrefix(a, "MODIFY ORDER BY ") {
			columns = append(columns, a)
		} else {
			standalone = append(standalone, a)
		}
	}
	queries := append(removals, standalone...)
	if len(columns) > 0 {
		queries = append([]string{strings.Join(columns, ", ")}, queries...)
	}
	for _, q := range queries {
		if err := (*s.CHConnection).Exec(ctx, "ALTER TABLE "+qualified(t)+" "+clusterClause(t.Cluster)+" "+q); err != nil {
			return fmt.Errorf("ALTER TABLE failed; refresh and retry after resolving the ClickHouse restriction, no automatic DROP/CREATE was attempted: %w", err)
		}
	}
	return nil
}

func hasConfiguredPrimaryKey(d *schema.ResourceData) bool {
	raw := d.GetRawConfig()
	if raw.IsNull() || !raw.IsKnown() {
		return false
	}
	v := raw.GetAttr("primary_key")
	return v.IsKnown() && !v.IsNull()
}
