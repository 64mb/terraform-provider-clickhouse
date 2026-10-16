package resourcetable

import (
	"encoding/json"
	"fmt"
	"strings"
)

type CHTable struct {
	SamplingKey      string     `ch:"sampling_key"`
	UUID             string     `ch:"uuid"`
	SortingKey       string     `ch:"sorting_key"`
	PrimaryKey       string     `ch:"primary_key"`
	PartitionKey     string     `ch:"partition_key"`
	CreateTableQuery string     `ch:"create_table_query"`
	Database         string     `ch:"database"`
	Name             string     `ch:"name"`
	EngineFull       string     `ch:"engine_full"`
	Engine           string     `ch:"engine"`
	Comment          string     `ch:"comment"`
	Columns          []CHColumn `ch:"columns"`
}

type CHColumn struct {
	Codec             string `ch:"compression_codec"`
	DefaultKind       string `ch:"default_kind"`
	DefaultExpression string `ch:"default_expression"`
	Comment           string `ch:"comment"`
	Database          string `ch:"database"`
	Table             string `ch:"table"`
	Name              string `ch:"name"`
	Type              string `ch:"type"`
}

type TableResource struct {
	SampleBy     string
	PrimaryKey   []string
	TTL          string
	Settings     map[string]string
	Database     string
	Name         string
	EngineFull   string
	Engine       string
	Cluster      string
	Comment      string
	EngineParams []string
	OrderBy      []string
	Columns      []interface{}
	PartitionBy  []PartitionByResource
}

type ColumnResource struct {
	Codec             string
	TTL               string
	DefaultKind       string
	DefaultExpression string
	Comment           string
	Name              string
	Type              string
}

type PartitionByResource struct {
	By                string
	PartitionFunction string
}

func (t *CHTable) ColumnsToResource() []interface{} {
	var columnResources []interface{}
	ttls := columnTTLs(t.CreateTableQuery)
	for _, column := range t.Columns {
		columnResource := map[string]interface{}{
			"codec":              codecArguments(column.Codec),
			"ttl":                ttls[column.Name],
			"name":               column.Name,
			"type":               column.Type,
			"default_kind":       column.DefaultKind,
			"default_expression": column.DefaultExpression,
			"comment":            column.Comment,
		}
		columnResources = append(columnResources, columnResource)
	}

	return columnResources
}

func (t *CHTable) ToResource() (*TableResource, error) {
	tableResource := TableResource{
		Database:   t.Database,
		Name:       t.Name,
		EngineFull: t.EngineFull,
		Engine:     t.Engine,
		Columns:    t.ColumnsToResource(),
	}

	tableResource.Comment = t.Comment
	var metadata map[string]interface{}
	raw := t.Comment
	if json.Unmarshal([]byte(raw), &metadata) != nil {
		metadata = nil
		raw = strings.ReplaceAll(raw, "\\'", "'")
		if json.Unmarshal([]byte(raw), &metadata) != nil {
			metadata = nil
		}
	}
	if comment, ok := metadata["comment"].(string); ok {
		if cluster, ok := metadata["cluster"].(string); ok {
			tableResource.Comment = comment
			tableResource.Cluster = cluster
		}
	}

	params, err := engineArguments(t.EngineFull, t.Engine)
	if err != nil {
		return nil, err
	}
	tableResource.EngineParams = params
	tableResource.OrderBy, err = keyExpressions(t.SortingKey)
	if err != nil {
		return nil, err
	}
	tableResource.PrimaryKey, err = keyExpressions(t.PrimaryKey)
	if err != nil {
		return nil, err
	}
	parts, err := keyExpressions(t.PartitionKey)
	if err != nil {
		return nil, err
	}
	for _, p := range parts {
		tableResource.PartitionBy = append(tableResource.PartitionBy, partitionResource(p))
	}
	clauses := sqlClauses(t.CreateTableQuery)
	if len(clauses) == 0 {
		clauses = sqlClauses(t.EngineFull)
	}
	if t.SortingKey == "" {
		tableResource.OrderBy, err = keyExpressions(clauses["ORDER BY"])
		if err != nil {
			return nil, err
		}
	}
	if t.PrimaryKey == "" {
		tableResource.PrimaryKey, err = keyExpressions(clauses["PRIMARY KEY"])
		if err != nil {
			return nil, err
		}
	}
	if t.PartitionKey == "" {
		items, e := keyExpressions(clauses["PARTITION BY"])
		if e != nil {
			return nil, e
		}
		for _, item := range items {
			tableResource.PartitionBy = append(tableResource.PartitionBy, partitionResource(item))
		}
	}
	tableResource.SampleBy = t.SamplingKey
	if tableResource.SampleBy == "" {
		tableResource.SampleBy = clauses["SAMPLE BY"]
	}
	tableResource.TTL = clauses["TTL"]
	tableResource.Settings = map[string]string{}
	if settings := clauses["SETTINGS"]; settings != "" {
		items, e := splitSQLList(settings)
		if e != nil {
			return nil, e
		}
		for _, item := range items {
			pos, e := sqlPositions(item, '=')
			if e != nil {
				return nil, e
			}
			if len(pos) > 0 {
				k := strings.Trim(strings.TrimSpace(item[:pos[0]]), "`\"")
				tableResource.Settings[k] = strings.TrimSpace(item[pos[0]+1:])
			}
		}
	}

	return &tableResource, nil
}

func (t *TableResource) GetColumnsResourceList() []ColumnResource {
	var columnResources []ColumnResource
	for _, column := range t.Columns {
		columnResources = append(columnResources, ColumnResource{
			Codec:             optionalColumnString(column, "codec"),
			TTL:               optionalColumnString(column, "ttl"),
			Name:              column.(map[string]interface{})["name"].(string),
			Type:              column.(map[string]interface{})["type"].(string),
			DefaultKind:       optionalColumnString(column, "default_kind"),
			DefaultExpression: optionalColumnString(column, "default_expression"),
			Comment:           optionalColumnString(column, "comment"),
		})
	}
	return columnResources
}

func (t *TableResource) SetPartitionBy(partitionBy []interface{}) {
	for _, partitionBy := range partitionBy {
		partitionByResource := PartitionByResource{
			By:                partitionBy.(map[string]interface{})["by"].(string),
			PartitionFunction: partitionBy.(map[string]interface{})["partition_function"].(string),
		}
		t.PartitionBy = append(t.PartitionBy, partitionByResource)
	}
}

func (t *TableResource) HasColumn(columnName string) bool {
	for _, column := range t.GetColumnsResourceList() {
		if column.Name == columnName {
			return true
		}
	}
	return false
}

func optionalColumnString(c interface{}, key string) string {
	s, _ := c.(map[string]interface{})[key].(string)
	return s
}
func engineArguments(full, engine string) ([]string, error) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(full), engine))
	if !strings.HasPrefix(rest, "(") {
		return []string{}, nil
	}
	var quote byte
	depth := 0
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				if i+1 < len(rest) && rest[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			quote = c
			continue
		}
		if c == '(' {
			depth++
		}
		if c == ')' {
			depth--
			if depth == 0 {
				return splitSQLList(rest[1:i])
			}
		}
	}
	return nil, fmt.Errorf("unbalanced engine arguments")
}
func keyExpressions(s string) ([]string, error) {
	s = strings.TrimSpace(s)
	if s == "tuple()" {
		return []string{}, nil
	}
	if strings.HasPrefix(s, "tuple(") && strings.HasSuffix(s, ")") {
		s = s[6 : len(s)-1]
	} else if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		s = s[1 : len(s)-1]
	}
	return splitSQLList(s)
}
func sqlClauses(s string) map[string]string {
	out := map[string]string{}
	scan := sqlWhitespace(s)
	positions, err := sqlPositions(scan, ' ')
	if err != nil {
		return out
	}
	positions = append([]int{-1}, positions...)
	start := 0
	if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(scan)), "CREATE ") {
		start = len(scan)
		for _, pos := range positions {
			i := pos + 1
			if keywordAt(scan, i, "ENGINE") || i+7 <= len(scan) && strings.EqualFold(scan[i:i+7], "ENGINE=") {
				start = i + len("ENGINE")
				break
			}
		}
	}
	type clause struct {
		name       string
		start, end int
	}
	var found []clause
	seen := map[string]bool{}
	for _, pos := range positions {
		i := pos + 1
		if i < start {
			continue
		}
		for _, name := range []string{"PARTITION BY", "PRIMARY KEY", "ORDER BY", "SAMPLE BY", "TTL", "SETTINGS", "COMMENT"} {
			if seen[name] || !keywordAt(scan, i, name) {
				continue
			}
			after := i + len(name)
			if name == "COMMENT" {
				j := after
				for j < len(scan) && scan[j] == ' ' {
					j++
				}
				if j == len(scan) || scan[j] != '\'' {
					continue
				}
			}
			if name == "SETTINGS" && !settingsHeader(scan[after:]) {
				continue
			}
			if len(found) > 0 && !sqlExpressionComplete(s[found[len(found)-1].end:i]) {
				continue
			}
			found = append(found, clause{name, i, after})
			seen[name] = true
			break
		}
	}
	for i, c := range found {
		end := len(s)
		if i+1 < len(found) {
			end = found[i+1].start
		}
		out[c.name] = strings.TrimSpace(s[c.end:end])
	}
	return out
}

func sqlWhitespace(s string) string {
	return strings.NewReplacer("\t", " ", "\r", " ", "\n", " ").Replace(s)
}
func keywordAt(s string, i int, word string) bool {
	return i >= 0 && i+len(word) <= len(s) && strings.EqualFold(s[i:i+len(word)], word) && (i+len(word) == len(s) || s[i+len(word)] == ' ')
}
func settingsHeader(s string) bool {
	p := typeParser{s: s}
	p.space()
	if p.i == len(s) {
		return false
	}
	if s[p.i] == '`' || s[p.i] == '"' {
		if _, err := p.quoted(s[p.i]); err != nil {
			return false
		}
	} else if p.ident() == "" {
		return false
	}
	return p.take('=')
}
func sqlExpressionComplete(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if strings.ContainsRune("+-*/%=<>!|&,.", rune(s[len(s)-1])) {
		return false
	}
	parts := strings.Fields(s)
	last := strings.ToUpper(parts[len(parts)-1])
	switch last {
	case "AS", "AND", "OR", "NOT", "IN", "BETWEEN", "WHEN", "THEN", "ELSE", "INTERVAL":
		return false
	}
	return true
}

func partitionResource(s string) PartitionByResource {
	for _, fn := range []string{"toYYYYMM", "toYYYYMMDD", "toYYYYMMDDhhmmss"} {
		if strings.HasPrefix(s, fn+"(") && strings.HasSuffix(s, ")") {
			args, err := splitSQLList(s[len(fn)+1 : len(s)-1])
			if err == nil && len(args) == 1 {
				return PartitionByResource{By: args[0], PartitionFunction: fn}
			}
		}
	}
	return PartitionByResource{By: s}
}
