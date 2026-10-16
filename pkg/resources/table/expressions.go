package resourcetable

import (
	"context"
	"fmt"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

// server formatting resolves equivalent syntax such as [INTERVAL] and [toIntervalDay]
func equalServerExpressions(ctx context.Context, conn *driver.Conn, a, b string, ttl bool) bool {
	if normalizeSQL(a) == normalizeSQL(b) {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	prefix := "SELECT "
	if ttl {
		prefix = "CREATE TABLE x (id UInt8) ENGINE = MergeTree ORDER BY tuple() TTL "
	}
	var equal uint8
	err := (*conn).QueryRow(ctx, "SELECT formatQuerySingleLine(?) = formatQuerySingleLine(?)", prefix+a, prefix+b).Scan(&equal)
	return err == nil && equal == 1
}
func preserveConfiguredExpressions(ctx context.Context, conn *driver.Conn, d *schema.ResourceData, r *TableResource) {
	for key, dst := range map[string]*[]string{"engine_params": &r.EngineParams, "order_by": &r.OrderBy, "primary_key": &r.PrimaryKey} {
		configured := d.Get(key).([]interface{})
		if len(configured) != len(*dst) {
			continue
		}
		for i, x := range configured {
			if equalServerExpressions(ctx, conn, x.(string), (*dst)[i], false) {
				(*dst)[i] = x.(string)
			}
		}
	}
	if configured := d.Get("ttl").(string); equalServerExpressions(ctx, conn, configured, r.TTL, true) {
		r.TTL = configured
	}
	if configured := d.Get("sample_by").(string); equalServerExpressions(ctx, conn, configured, r.SampleBy, false) {
		r.SampleBy = configured
	}
	configuredCols := d.Get("column").([]interface{})
	byName := map[string]map[string]interface{}{}
	for _, x := range configuredCols {
		m := x.(map[string]interface{})
		byName[m["name"].(string)] = m
	}
	for _, x := range r.Columns {
		m := x.(map[string]interface{})
		if c, ok := byName[m["name"].(string)]; ok {
			for _, key := range []string{"default_expression", "codec", "ttl"} {
				a, _ := c[key].(string)
				b, _ := m[key].(string)
				if equalServerExpressions(ctx, conn, a, b, false) {
					m[key] = a
				}
			}
		}
	}
	configuredParts := d.Get("partition_by").([]interface{})
	if len(configuredParts) == len(r.PartitionBy) {
		for i, x := range configuredParts {
			m := x.(map[string]interface{})
			p := PartitionByResource{By: m["by"].(string), PartitionFunction: m["partition_function"].(string)}
			expression := func(p PartitionByResource) string {
				if p.PartitionFunction != "" {
					return p.PartitionFunction + "(" + p.By + ")"
				}
				return p.By
			}
			if equalServerExpressions(ctx, conn, expression(p), expression(r.PartitionBy[i]), false) {
				r.PartitionBy[i] = p
			}
		}
	}
	for k, v := range d.Get("settings").(map[string]interface{}) {
		if actual, ok := r.Settings[k]; ok && equalServerExpressions(ctx, conn, v.(string), actual, false) {
			r.Settings[k] = v.(string)
		}
	}
}

func validateTableDiff(ctx context.Context, d *schema.ResourceDiff, meta interface{}) error {
	for _, k := range []string{"database", "name", "cluster", "engine", "engine_params", "column", "order_by", "partition_by", "primary_key", "ttl", "sample_by", "settings"} {
		if !d.NewValueKnown(k) {
			return nil
		}
	}
	if d.Id() != "" {
		for _, k := range []string{"database", "name", "cluster"} {
			if d.HasChange(k) {
				return fmt.Errorf("changing %s requires an external migration and import", k)
			}
		}
	}
	return validateTable(tableFromData(d))
}

// the server writes this default into every MergeTree DDL even when it was omitted
func omitImplicitDefaults(configured TableResource, actual *TableResource) {
	if _, ok := configured.Settings["index_granularity"]; !ok && normalizeSQL(actual.Settings["index_granularity"]) == "8192" {
		delete(actual.Settings, "index_granularity")
	}
}

func equalServerExpressionLists(ctx context.Context, conn *driver.Conn, a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !equalServerExpressions(ctx, conn, a[i], b[i], false) {
			return false
		}
	}
	return true
}
