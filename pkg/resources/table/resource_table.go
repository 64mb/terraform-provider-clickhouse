package resourcetable

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	cty "github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
)

func expressionSchema() *schema.Schema {
	return &schema.Schema{Type: schema.TypeString, Optional: true, DiffSuppressFunc: equivalentSQL, ValidateDiagFunc: validateFragment}
}
func expressionList() *schema.Schema {
	return &schema.Schema{Type: schema.TypeList, Optional: true, Elem: &schema.Schema{Type: schema.TypeString}, DiffSuppressFunc: equivalentSQL}
}
func equivalentSQL(k, old, new string, d *schema.ResourceData) bool {
	return normalizeSQL(old) == normalizeSQL(new)
}
func equivalentEngineParam(k, old, next string, d *schema.ResourceData) bool {
	if equivalentSQL(k, old, next, d) {
		return true
	}
	return k == "engine_params.0" && replicationPathEquivalent(d.Get("engine").(string), d.Get("replication_path_macros_enabled").(bool), next, old, d.Get("database").(string), d.Get("name").(string))
}
func validateFragment(v interface{}, p cty.Path) diag.Diagnostics {
	return diag.FromErr(validateSQLFragment(v.(string)))
}

func ResourceTable() *schema.Resource {
	return &schema.Resource{
		Description:   "Manages ClickHouse tables with explicit ALTER and opt-in maintenance rebuilds",
		CreateContext: resourceTableCreate, ReadContext: resourceTableRead, UpdateContext: resourceTableUpdate, DeleteContext: resourceTableDelete,
		Importer:      &schema.ResourceImporter{StateContext: importTable},
		CustomizeDiff: validateTableDiff,
		Schema: map[string]*schema.Schema{
			"database":                        {Type: schema.TypeString, Required: true, Description: "Database name"},
			"name":                            {Type: schema.TypeString, Required: true, Description: "Table name"},
			"cluster":                         {Type: schema.TypeString, Optional: true, Computed: true, Description: "Explicit cluster override, otherwise provider default_cluster is used on creation"},
			"comment":                         {Type: schema.TypeString, Optional: true, Description: "Table comment"},
			"engine":                          {Type: schema.TypeString, Required: true, ValidateDiagFunc: ValidateOnClusterEngine, Description: "MergeTree family or Distributed"},
			"engine_params":                   {Type: schema.TypeList, Optional: true, DiffSuppressFunc: equivalentEngineParam, Elem: &schema.Schema{Type: schema.TypeString}, Description: "Engine arguments as SQL expressions"},
			"replication_path_macros_enabled": {Type: schema.TypeBool, Computed: true, Description: "Whether server metadata confirms built-in database and table replication path expansion is enabled"},
			"order_by":                        expressionList(),
			"primary_key":                     primaryKeySchema(),
			"partition_by": {Type: schema.TypeList, Optional: true, Elem: &schema.Resource{Schema: map[string]*schema.Schema{
				"by":                 {Type: schema.TypeString, Required: true, DiffSuppressFunc: equivalentSQL, ValidateDiagFunc: validateFragment},
				"partition_function": {Type: schema.TypeString, Optional: true, ValidateDiagFunc: ValidatePartitionBy},
			}}},
			"ttl":                expressionSchema(),
			"sample_by":          expressionSchema(),
			"uuid":               {Type: schema.TypeString, Computed: true, Description: "ClickHouse table UUID"},
			"create_table_query": {Type: schema.TypeString, Computed: true, Description: "Server DDL, including unmanaged metadata for import inspection"},
			"settings":           {Type: schema.TypeMap, Optional: true, DiffSuppressFunc: equivalentSQL, Elem: &schema.Schema{Type: schema.TypeString}, Description: "MergeTree settings, values are SQL literals or expressions"},
			"column": {Type: schema.TypeList, Required: true, MinItems: 1, Elem: &schema.Resource{Schema: map[string]*schema.Schema{
				"name": {Type: schema.TypeString, Required: true},
				"type": {Type: schema.TypeString, Required: true, ValidateDiagFunc: ValidateType, DiffSuppressFunc: func(k, a, b string, d *schema.ResourceData) bool {
					x, e := CanonicalType(a)
					y, f := CanonicalType(b)
					return e == nil && f == nil && x == y
				}},
				"default_kind":       {Type: schema.TypeString, Optional: true, ValidateFunc: validation.StringInSlice([]string{"", "DEFAULT", "MATERIALIZED", "ALIAS"}, false)},
				"default_expression": expressionSchema(),
				"codec":              expressionSchema(),
				"ttl":                expressionSchema(),
				"comment":            {Type: schema.TypeString, Optional: true},
			}}},
			"allow_column_drop": {Type: schema.TypeBool, Optional: true, Default: false, Description: "Explicitly allow removal of columns and their data"},
			"rebuild": {Type: schema.TypeList, Optional: true, MaxItems: 1, Description: "Offline rebuild contract: stop and drain every writer externally before acknowledging maintenance, keep them stopped through recovery", Elem: &schema.Resource{Schema: map[string]*schema.Schema{
				"enabled":                  {Type: schema.TypeBool, Required: true},
				"maintenance_acknowledged": {Type: schema.TypeBool, Required: true, Description: "Confirms all direct, async and indirect writers are stopped and drained until apply/recovery completes"},
				"operation_id":             {Type: schema.TypeString, Required: true, ValidateFunc: validation.StringIsNotWhiteSpace, Description: "Unique migration identifier, retain on retry and change for each new rebuild"},
			}}},
			"rebuild_backup_table": {Type: schema.TypeString, Computed: true, Description: "Retained old table after EXCHANGE, remove manually only after validating the migration"},
		},
	}
}

func importTable(ctx context.Context, d *schema.ResourceData, meta interface{}) ([]*schema.ResourceData, error) {
	parts := strings.Split(d.Id(), ":")
	if len(parts) == 2 {
		parts = append([]string{""}, parts...)
	}
	if len(parts) != 3 || parts[1] == "" || parts[2] == "" {
		return nil, fmt.Errorf("import ID must be database:table or cluster:database:table")
	}
	for i, k := range []string{"cluster", "database", "name"} {
		if err := d.Set(k, parts[i]); err != nil {
			return nil, err
		}
	}
	d.SetId(strings.Join(parts, ":"))
	return []*schema.ResourceData{d}, nil
}

func tableFromData(d interface{ Get(string) interface{} }) TableResource {
	t := TableResource{Database: d.Get("database").(string), Name: d.Get("name").(string), Cluster: d.Get("cluster").(string), Engine: d.Get("engine").(string), Comment: d.Get("comment").(string), Columns: d.Get("column").([]interface{}), TTL: d.Get("ttl").(string), SampleBy: d.Get("sample_by").(string), Settings: map[string]string{}}
	for k, dst := range map[string]*[]string{"engine_params": &t.EngineParams, "order_by": &t.OrderBy, "primary_key": &t.PrimaryKey} {
		*dst = common.MapArrayInterfaceToArrayOfStrings(d.Get(k).([]interface{}))
	}
	t.SetPartitionBy(d.Get("partition_by").([]interface{}))
	for k, v := range d.Get("settings").(map[string]interface{}) {
		t.Settings[k] = v.(string)
	}
	return t
}
func storedComment(comment, cluster string) string {
	b, _ := json.Marshal(map[string]string{"comment": comment, "cluster": cluster})
	return string(b)
}
func resourceTableCreate(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*common.ApiClient)
	t := tableFromData(d)
	if t.Cluster == "" {
		t.Cluster = client.DefaultCluster
	}
	if err := validateTable(t); err != nil {
		return diag.FromErr(err)
	}
	t.Comment = storedComment(t.Comment, t.Cluster)
	s := CHTableService{CHConnection: client.ClickhouseConnection}
	if err := s.CreateTable(ctx, t); err != nil {
		return diag.FromErr(err)
	}
	d.SetId(t.Cluster + ":" + t.Database + ":" + t.Name)
	if err := d.Set("cluster", t.Cluster); err != nil {
		return diag.FromErr(err)
	}
	return resourceTableRead(ctx, d, meta)
}
func resourceTableRead(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*common.ApiClient)
	s := CHTableService{CHConnection: client.ClickhouseConnection}
	t, err := s.GetTable(ctx, d.Get("database").(string), d.Get("name").(string))
	if errors.Is(err, sql.ErrNoRows) {
		d.SetId("")
		return nil
	}
	if err != nil {
		return diag.FromErr(err)
	}
	r, err := t.ToResource()
	if err != nil {
		return diag.FromErr(err)
	}
	// preserve the configured [Nested] grouping while ClickHouse exposes flattened arrays
	if cluster := d.Get("cluster").(string); cluster != "" {
		r.Cluster = cluster
	}
	omitImplicitDefaults(tableFromData(d), r)
	macrosEnabled := replicationPathMacrosEnabled(ctx, client.ClickhouseConnection, r.Engine)
	if err := d.Set("replication_path_macros_enabled", macrosEnabled); err != nil {
		return diag.FromErr(err)
	}
	preserveConfiguredExpressions(ctx, client.ClickhouseConnection, d, r, macrosEnabled)
	r.Columns = restoreNestedColumns(d.Get("column").([]interface{}), r.Columns)
	partition := []interface{}{}
	for _, p := range r.PartitionBy {
		partition = append(partition, map[string]interface{}{"by": p.By, "partition_function": p.PartitionFunction})
	}

	if m := markerOf(t); m != nil && m.SourceUUID != t.UUID {
		if err := d.Set("rebuild_backup_table", m.Backup); err != nil {
			return diag.FromErr(err)
		}
	}
	values := map[string]interface{}{"allow_column_drop": d.Get("allow_column_drop"), "sample_by": r.SampleBy, "uuid": t.UUID, "create_table_query": t.CreateTableQuery, "database": r.Database, "name": r.Name, "cluster": r.Cluster, "comment": r.Comment, "engine": r.Engine, "engine_params": r.EngineParams, "order_by": r.OrderBy, "primary_key": r.PrimaryKey, "partition_by": partition, "column": r.Columns, "ttl": r.TTL, "settings": r.Settings}
	for k, v := range values {
		if err := d.Set(k, v); err != nil {
			return diag.FromErr(fmt.Errorf("setting %s: %w", k, err))
		}
	}
	return nil
}
func resourceTableDelete(ctx context.Context, d *schema.ResourceData, meta interface{}) diag.Diagnostics {
	client := meta.(*common.ApiClient)
	s := CHTableService{CHConnection: client.ClickhouseConnection}
	t := tableFromData(d)
	if err := s.DeleteTable(ctx, t); err != nil {
		return diag.FromErr(err)
	}
	d.SetId("")
	return nil
}

func primaryKeySchema() *schema.Schema { s := expressionList(); s.Computed = true; return s }
