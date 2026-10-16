package resourcetable

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type rebuildMarker struct {
	Operation   string `json:"operation"`
	SourceUUID  string `json:"source_uuid"`
	Fingerprint string `json:"fingerprint"`
	Backup      string `json:"backup"`
}
type rebuildComment struct {
	Comment string         `json:"comment"`
	Cluster string         `json:"cluster"`
	Rebuild *rebuildMarker `json:"terraform_rebuild,omitempty"`
}

func markerOf(t *CHTable) *rebuildMarker {
	var c rebuildComment
	if json.Unmarshal([]byte(t.Comment), &c) != nil {
		return nil
	}
	return c.Rebuild
}
func rebuildFingerprint(t TableResource) string {
	t.EngineFull = ""
	t.Columns = append([]interface{}{}, t.Columns...)
	for i, col := range t.GetColumnsResourceList() {
		col.Type, _ = CanonicalType(col.Type)
		t.Columns[i] = map[string]interface{}{"name": col.Name, "type": col.Type, "default_kind": col.DefaultKind, "default_expression": normalizeSQL(col.DefaultExpression), "comment": col.Comment, "codec": normalizeSQL(col.Codec), "ttl": normalizeSQL(col.TTL)}
	}
	b, _ := json.Marshal(t)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func rebuildName(t TableResource, operation string) string {
	h := sha256.Sum256([]byte(t.Database + "\x00" + t.Name + "\x00" + operation))
	return "__tf_rebuild_" + hex.EncodeToString(h[:16])
}

// the maintenance check supplements the operator's external stop-and-drain contract
func (s *CHTableService) maintenanceCheck(ctx context.Context, t TableResource) error {
	conn := *s.CHConnection
	var active uint64
	queries := []struct {
		sql  string
		args []interface{}
	}{
		{"SELECT count() FROM system.processes WHERE query_kind NOT IN ('Select', 'Explain')", nil},
		{"SELECT count() FROM system.asynchronous_inserts", nil},
		{"SELECT count() FROM system.mutations WHERE database = ? AND table = ? AND NOT is_done", []interface{}{t.Database, t.Name}},
	}
	for _, q := range queries {
		if err := conn.QueryRow(ctx, q.sql, q.args...).Scan(&active); err != nil {
			return fmt.Errorf("checking write quiescence: %w", err)
		}
		if active != 0 {
			return fmt.Errorf("maintenance rebuild requires drained writers, async inserts and mutations; found %d active operations", active)
		}
	}
	return nil
}
func (s *CHTableService) rebuildPreflight(ctx context.Context, live *CHTable, next TableResource) error {
	if live.Engine != "MergeTree" || next.Engine != "MergeTree" || next.Cluster != "" {
		return fmt.Errorf("maintenance rebuild supports only standalone MergeTree to MergeTree without ON CLUSTER; Replicated, Distributed and reducing/aggregating engines require an external topology-aware migration")
	}
	var engine string
	if err := (*s.CHConnection).QueryRow(ctx, "SELECT engine FROM system.databases WHERE name = ?", next.Database).Scan(&engine); err != nil {
		return err
	}
	if engine != "Atomic" {
		return fmt.Errorf("maintenance rebuild requires an Atomic database, got %s", engine)
	}
	var count uint64
	// reject indirect writers and dependencies conservatively rather than guessing name binding
	q := "SELECT count() FROM system.tables WHERE database NOT IN ('system', 'INFORMATION_SCHEMA', 'information_schema') AND (engine IN ('View', 'MaterializedView', 'LiveView', 'WindowView', 'Distributed', 'Buffer', 'Dictionary') OR length(dependencies_table) > 0)"
	if err := (*s.CHConnection).QueryRow(ctx, q).Scan(&count); err != nil {
		return fmt.Errorf("checking table dependencies: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("maintenance rebuild requires a server without user views, dictionaries, Buffer or Distributed tables and dependencies; migrate dependent topology externally")
	}
	for _, q := range []string{"SELECT count() FROM system.projections WHERE database = ? AND table = ?", "SELECT count() FROM system.data_skipping_indices WHERE database = ? AND table = ?"} {
		if err := (*s.CHConnection).QueryRow(ctx, q, live.Database, live.Name).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			return fmt.Errorf("maintenance rebuild does not support projections or skipping indices")
		}
	}
	old, err := live.ToResource()
	if err != nil {
		return err
	}
	if old.SampleBy != "" || next.SampleBy != "" {
		return fmt.Errorf("maintenance rebuild does not support SAMPLE BY tables")
	}
	if old.TTL != "" || next.TTL != "" {
		return fmt.Errorf("maintenance rebuild does not support TTL tables; remove TTL and wait for background work before migration")
	}
	upper := strings.ToUpper(strings.Join(strings.Fields(live.CreateTableQuery), " "))
	for _, word := range []string{" TTL ", "CODEC(", "CODEC (", "CONSTRAINT ", "SAMPLE BY", "STATISTICS "} {
		if strings.Contains(upper, word) {
			return fmt.Errorf("maintenance rebuild cannot preserve %s metadata; migrate this table externally", strings.TrimSpace(word))
		}
	}
	for _, t := range []TableResource{*old, next} {
		for _, c := range t.GetColumnsResourceList() {
			if c.Codec != "" || c.TTL != "" {
				return fmt.Errorf("maintenance rebuild does not support column codecs or TTL")
			}
			if c.DefaultKind != "" && c.DefaultKind != "DEFAULT" {
				return fmt.Errorf("maintenance rebuild does not support %s columns", c.DefaultKind)
			}
		}
	}
	return s.maintenanceCheck(ctx, next)
}

type rowDigest struct{ Count, Xor, Sum uint64 }

func (s *CHTableService) digest(ctx context.Context, t TableResource, expressions []string) (rowDigest, error) {
	var d rowDigest
	query := "SELECT count(), groupBitXor(cityHash64(tuple(" + strings.Join(expressions, ", ") + "))), sum(cityHash64(tuple(" + strings.Join(expressions, ", ") + "))) FROM " + qualified(t)
	err := (*s.CHConnection).QueryRow(ctx, query).Scan(&d.Count, &d.Xor, &d.Sum)
	return d, err
}
func (s *CHTableService) completedRebuild(ctx context.Context, live *CHTable, next TableResource, cfg map[string]interface{}) (string, bool, error) {
	m := markerOf(live)
	if m == nil || m.Operation != cfg["operation_id"].(string) {
		return "", false, nil
	}
	if m.Fingerprint != rebuildFingerprint(next) {
		return "", true, fmt.Errorf("operation_id was already used for a different target definition; use a new operation_id")
	}
	if err := s.verifyRebuildDefinition(ctx, live, next); err != nil {
		return "", true, fmt.Errorf("completed rebuild definition no longer matches journal: %w", err)
	}
	backup, err := s.GetTable(ctx, next.Database, m.Backup)
	if err != nil {
		return "", true, fmt.Errorf("rebuild was switched but retained source cannot be verified: %w", err)
	}
	if backup.UUID != m.SourceUUID {
		return "", true, fmt.Errorf("retained rebuild source UUID does not match journal; manual recovery required")
	}
	return m.Backup, true, nil
}
func (s *CHTableService) rebuildTable(ctx context.Context, live *CHTable, next TableResource, cfg map[string]interface{}, allowDrop bool) (string, error) {
	if !cfg["maintenance_acknowledged"].(bool) {
		return "", fmt.Errorf("stop and drain ALL writers externally, including async queues and indirect writers, then set rebuild.maintenance_acknowledged = true; keep maintenance active through every retry")
	}
	if backup, done, err := s.completedRebuild(ctx, live, next, cfg); done {
		return backup, err
	}
	if err := s.rebuildPreflight(ctx, live, next); err != nil {
		return "", err
	}
	operation := cfg["operation_id"].(string)
	stage := next
	stage.Name = rebuildName(next, operation)
	marker := &rebuildMarker{Operation: operation, SourceUUID: live.UUID, Fingerprint: rebuildFingerprint(next), Backup: stage.Name}
	comment, _ := json.Marshal(rebuildComment{Comment: next.Comment, Cluster: next.Cluster, Rebuild: marker})
	stage.Comment = string(comment)
	existing, err := s.GetTable(ctx, next.Database, stage.Name)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if existing != nil {
		m := markerOf(existing)
		if m == nil || *m != *marker {
			return "", fmt.Errorf("rebuild staging name is occupied by a different operation; refusing to modify %s", stage.Name)
		}
		if err := s.verifyRebuildDefinition(ctx, existing, next); err != nil {
			return "", fmt.Errorf("owned staging definition changed; refusing to truncate: %w", err)
		}
		// only an owned pre-switch staging table can be reset after an uncertain copy
		if err := (*s.CHConnection).Exec(ctx, "TRUNCATE TABLE "+qualified(stage)); err != nil {
			return "", err
		}
	} else {
		if err := s.CreateTable(ctx, stage); err != nil {
			return "", err
		}
	}
	old, err := live.ToResource()
	if err != nil {
		return "", err
	}
	oldCols, err := expandedColumns(old.GetColumnsResourceList())
	if err != nil {
		return "", err
	}
	newCols, err := expandedColumns(next.GetColumnsResourceList())
	if err != nil {
		return "", err
	}
	oldNames := map[string]bool{}
	newNames := map[string]bool{}
	for _, c := range oldCols {
		oldNames[c.Name] = true
	}
	for _, c := range newCols {
		newNames[c.Name] = true
	}
	for _, c := range oldCols {
		if !newNames[c.Name] && !allowDrop {
			return "", fmt.Errorf("rebuild would remove column %q; set allow_column_drop = true explicitly", c.Name)
		}
	}
	names := []string{}
	sourceExpr := []string{}
	targetExpr := []string{}
	for _, c := range newCols {
		if oldNames[c.Name] {
			names = append(names, quoteIdent(c.Name))
			sourceExpr = append(sourceExpr, "CAST("+quoteIdent(c.Name)+" AS "+c.Type+")")
			targetExpr = append(targetExpr, quoteIdent(c.Name))
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("maintenance rebuild requires at least one common stored column")
	}
	before, err := s.digest(ctx, *old, sourceExpr)
	if err != nil {
		return "", err
	}
	if err := (*s.CHConnection).Exec(ctx, "INSERT INTO "+qualified(stage)+" ("+strings.Join(names, ", ")+") SELECT "+strings.Join(sourceExpr, ", ")+" FROM "+qualified(*old)); err != nil {
		return "", fmt.Errorf("copy failed, original table retained; retry with the same operation_id while writers remain stopped: %w", err)
	}
	copied, err := s.digest(ctx, stage, targetExpr)
	if err != nil {
		return "", err
	}
	after, err := s.digest(ctx, *old, sourceExpr)
	if err != nil {
		return "", err
	}
	if before != after || before != copied {
		return "", fmt.Errorf("rebuild row count/checksum verification failed; original and staging tables retained, keep writers stopped and retry")
	}
	currentSource, err := s.GetTable(ctx, next.Database, next.Name)
	if err != nil {
		return "", err
	}
	if currentSource.UUID != live.UUID {
		return "", fmt.Errorf("source UUID changed during rebuild; refusing EXCHANGE")
	}
	currentStage, err := s.GetTable(ctx, next.Database, stage.Name)
	if err != nil {
		return "", err
	}
	if err := s.verifyRebuildDefinition(ctx, currentStage, next); err != nil {
		return "", err
	}
	if err := s.maintenanceCheck(ctx, next); err != nil {
		return "", err
	}
	if err := (*s.CHConnection).Exec(ctx, "EXCHANGE TABLES "+qualified(next)+" AND "+qualified(stage)); err != nil {
		return "", fmt.Errorf("EXCHANGE outcome may be uncertain; retain maintenance and retry with the same operation_id: %w", err)
	}
	switched, err := s.GetTable(ctx, next.Database, next.Name)
	if err != nil {
		return "", err
	}
	backup, done, err := s.completedRebuild(ctx, switched, next, cfg)
	if !done && err == nil {
		err = fmt.Errorf("EXCHANGE journal not found; manual recovery required")
	}
	return backup, err
}

func (s *CHTableService) verifyRebuildDefinition(ctx context.Context, live *CHTable, expected TableResource) error {
	if err := s.rebuildPreflight(ctx, live, expected); err != nil {
		return err
	}
	actual, err := live.ToResource()
	if err != nil {
		return err
	}
	if actual.Comment != expected.Comment || actual.Cluster != expected.Cluster {
		return fmt.Errorf("staging comment metadata does not match target")
	}
	if actual.Engine != expected.Engine {
		return fmt.Errorf("staging engine does not match target")
	}
	expectedCols, err := expandedColumns(expected.GetColumnsResourceList())
	if err != nil {
		return err
	}
	actualCols := actual.GetColumnsResourceList()
	if len(expectedCols) != len(actualCols) {
		return fmt.Errorf("staging column count does not match target")
	}
	for i, c := range expectedCols {
		a := actualCols[i]
		at, e := CanonicalType(a.Type)
		if e != nil {
			return e
		}
		ct, e := CanonicalType(c.Type)
		if e != nil {
			return e
		}
		if a.Name != c.Name || at != ct || a.DefaultKind != c.DefaultKind || a.Comment != c.Comment || normalizeSQL(a.Codec) != normalizeSQL(c.Codec) || !equalServerExpressions(ctx, s.CHConnection, a.DefaultExpression, c.DefaultExpression, false) {
			return fmt.Errorf("staging column %q does not match target", c.Name)
		}
	}
	if len(expected.PrimaryKey) == 0 {
		expected.PrimaryKey = expected.OrderBy
	}
	if !equalServerExpressionLists(ctx, s.CHConnection, actual.EngineParams, expected.EngineParams) || !equalServerExpressionLists(ctx, s.CHConnection, actual.OrderBy, expected.OrderBy) || !equalServerExpressionLists(ctx, s.CHConnection, actual.PrimaryKey, expected.PrimaryKey) || !equalServerExpressions(ctx, s.CHConnection, strings.TrimPrefix(buildPartitionBySentence(actual.PartitionBy), "PARTITION BY "), strings.TrimPrefix(buildPartitionBySentence(expected.PartitionBy), "PARTITION BY "), false) {
		return fmt.Errorf("staging keys do not match target")
	}
	omitImplicitDefaults(expected, actual)
	if len(actual.Settings) != len(expected.Settings) {
		return fmt.Errorf("staging settings do not match target")
	}
	for k, v := range expected.Settings {
		if !equalServerExpressions(ctx, s.CHConnection, actual.Settings[k], v, false) {
			return fmt.Errorf("staging setting %q does not match target", k)
		}
	}
	return nil
}
