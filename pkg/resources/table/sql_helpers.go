package resourcetable

import (
	"fmt"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"sort"
	"strings"
	"unicode"
)

func quoteIdent(s string) string {
	return "`" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "`", "\\`") + "`"
}
func quoteString(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "'", "\\'") + "'"
}
func qualified(t TableResource) string { return quoteIdent(t.Database) + "." + quoteIdent(t.Name) }
func clusterClause(s string) string {
	if s == "" {
		return ""
	}
	return common.GetClusterStatement(s)
}

// find delimiters outside literals and nested expressions
func sqlPositions(s string, delimiter byte) ([]int, error) {
	var positions []int
	var stack []byte
	var quote byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				if i+1 < len(s) && s[i+1] == quote {
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
		if c == ';' || (c == '-' && i+1 < len(s) && s[i+1] == '-') || (c == '/' && i+1 < len(s) && s[i+1] == '*') || c == '#' {
			return nil, fmt.Errorf("SQL statements and comments are not allowed")
		}
		switch c {
		case '(', '[':
			stack = append(stack, c)
		case ')', ']':
			if len(stack) == 0 || (c == ')' && stack[len(stack)-1] != '(') || (c == ']' && stack[len(stack)-1] != '[') {
				return nil, fmt.Errorf("unbalanced SQL expression")
			}
			stack = stack[:len(stack)-1]
		}
		if c == delimiter && len(stack) == 0 {
			positions = append(positions, i)
		}
	}
	if quote != 0 || len(stack) > 0 {
		return nil, fmt.Errorf("unbalanced SQL expression")
	}
	return positions, nil
}
func validateSQLFragment(s string) error { _, err := sqlPositions(s, 0); return err }
func splitSQLList(s string) ([]string, error) {
	positions, err := sqlPositions(s, ',')
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(s) == "" {
		return []string{}, nil
	}
	out := []string{}
	start := 0
	for _, i := range positions {
		out = append(out, strings.TrimSpace(s[start:i]))
		start = i + 1
	}
	out = append(out, strings.TrimSpace(s[start:]))
	return out, nil
}
func normalizeSQL(s string) string {
	var out strings.Builder
	token := func(c byte) bool {
		return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
	}
	for i := 0; i < len(s); {
		c := s[i]
		if unicode.IsSpace(rune(c)) {
			j := i + 1
			for j < len(s) && unicode.IsSpace(rune(s[j])) {
				j++
			}
			if i > 0 && j < len(s) && (token(s[i-1]) || s[i-1] == '`' || s[i-1] == '\'' || s[i-1] == '"') && (token(s[j]) || s[j] == '`' || s[j] == '\'' || s[j] == '"') {
				out.WriteByte(' ')
			}
			i = j
			continue
		}
		if c == '\'' || c == '"' || c == '`' {
			start := i
			q := c
			i++
			for i < len(s) {
				if s[i] == '\\' {
					i += 2
					continue
				}
				if s[i] == q {
					i++
					if i < len(s) && s[i] == q {
						i++
						continue
					}
					break
				}
				i++
			}
			if i > len(s) {
				i = len(s)
			}
			value := s[start:i]
			if q == '`' && len(value) >= 2 {
				inner := value[1 : len(value)-1]
				valid := len(inner) > 0
				for j := range inner {
					if !token(inner[j]) || (j == 0 && inner[j] >= '0' && inner[j] <= '9') {
						valid = false
					}
				}
				if valid {
					value = inner
				}
			}
			out.WriteString(value)
			continue
		}
		out.WriteByte(c)
		i++
	}
	return out.String()
}

func buildColumnsSentence(cols []ColumnResource) []string {
	out := []string{}
	for _, c := range cols {
		s := quoteIdent(c.Name) + " " + c.Type
		if c.DefaultKind != "" {
			s += " " + c.DefaultKind + " " + c.DefaultExpression
		}
		if c.Comment != "" {
			s += " COMMENT " + quoteString(c.Comment)
		}
		if c.Codec != "" {
			s += " CODEC(" + c.Codec + ")"
		}
		if c.TTL != "" {
			s += " TTL " + c.TTL
		}
		out = append(out, s)
	}
	return out
}
func buildPartitionBySentence(parts []PartitionByResource) string {
	items := []string{}
	for _, p := range parts {
		s := p.By
		if p.PartitionFunction != "" {
			s = p.PartitionFunction + "(" + s + ")"
		}
		items = append(items, s)
	}
	if len(items) == 0 {
		return ""
	}
	return "PARTITION BY (" + strings.Join(items, ", ") + ")"
}
func buildOrderBySentence(items []string) string {
	if len(items) == 0 {
		return ""
	}
	return "ORDER BY (" + strings.Join(items, ", ") + ")"
}
func buildCreateOnClusterSentence(t TableResource) string {
	s := "CREATE TABLE " + qualified(t) + " " + clusterClause(t.Cluster) + " (" + strings.Join(buildColumnsSentence(t.GetColumnsResourceList()), ", ") + ") ENGINE = " + t.Engine + "(" + strings.Join(t.EngineParams, ", ") + ")"
	if p := buildPartitionBySentence(t.PartitionBy); p != "" {
		s += " " + p
	}
	if p := buildOrderBySentence(t.OrderBy); p != "" {
		s += " " + p
	} else if strings.Contains(t.Engine, "MergeTree") {
		s += " ORDER BY tuple()"
	}
	if len(t.PrimaryKey) > 0 {
		s += " PRIMARY KEY (" + strings.Join(t.PrimaryKey, ", ") + ")"
	}
	if t.SampleBy != "" {
		s += " SAMPLE BY " + t.SampleBy
	}
	if t.TTL != "" {
		s += " TTL " + t.TTL
	}
	keys := []string{}
	for k := range t.Settings {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		items := []string{}
		for _, k := range keys {
			items = append(items, quoteIdent(k)+" = "+t.Settings[k])
		}
		s += " SETTINGS " + strings.Join(items, ", ")
	}
	s += " COMMENT " + quoteString(t.Comment)
	return s
}
func validateTable(t TableResource) error {
	if t.Name == "" || t.Database == "" {
		return fmt.Errorf("database and table names must not be empty")
	}
	if t.Engine != "Distributed" && !mergeTreeEngine(t.Engine) {
		return fmt.Errorf("unsupported engine %q", t.Engine)
	}
	if t.Engine == "" {
		return fmt.Errorf("engine must not be empty")
	}
	for _, c := range t.Engine {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
			return fmt.Errorf("invalid engine name")
		}
	}
	if _, err := expandedColumns(t.GetColumnsResourceList()); err != nil {
		return err
	}
	fragments := append([]string{}, t.EngineParams...)
	fragments = append(fragments, t.OrderBy...)
	fragments = append(fragments, t.PrimaryKey...)
	fragments = append(fragments, t.TTL, t.SampleBy)
	seen := map[string]bool{}
	for _, c := range t.GetColumnsResourceList() {
		if c.Name == "" || seen[c.Name] {
			return fmt.Errorf("empty or duplicate column name %q", c.Name)
		}
		seen[c.Name] = true
		if _, err := CanonicalType(c.Type); err != nil {
			return fmt.Errorf("column %q: %w", c.Name, err)
		}
		switch c.DefaultKind {
		case "", "DEFAULT", "MATERIALIZED", "ALIAS", "EPHEMERAL":
		default:
			return fmt.Errorf("invalid default kind %q", c.DefaultKind)
		}
		if (c.DefaultKind == "") != (c.DefaultExpression == "") {
			return fmt.Errorf("default kind and expression must be specified together")
		}
		fragments = append(fragments, c.Type, c.DefaultExpression, c.TTL, c.Codec)
	}
	for _, p := range t.PartitionBy {
		fragments = append(fragments, p.By)
		if p.PartitionFunction != "" {
			for _, c := range p.PartitionFunction {
				if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '_' {
					return fmt.Errorf("invalid partition function")
				}
			}
		}
	}
	for _, v := range t.Settings {
		fragments = append(fragments, v)
	}
	for _, s := range fragments {
		if err := validateSQLFragment(s); err != nil {
			return err
		}
	}
	return nil
}

func codecArguments(codec string) string {
	codec = strings.TrimSpace(codec)
	if len(codec) > 7 && strings.EqualFold(codec[:5], "CODEC") && codec[5] == '(' && codec[len(codec)-1] == ')' {
		return strings.TrimSpace(codec[6 : len(codec)-1])
	}
	return codec
}

// reads column TTL expressions from the balanced definition list in [CREATE TABLE]
func columnTTLs(create string) map[string]string {
	out := map[string]string{}
	start := -1
	end := -1
	depth := 0
	var quote byte
	for i := 0; i < len(create); i++ {
		c := create[i]
		if quote != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == quote {
				if i+1 < len(create) && create[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '`' || c == '"' {
			quote = c
			continue
		}
		if c == '(' {
			if start < 0 {
				start = i
			}
			depth++
		}
		if c == ')' {
			depth--
			if start >= 0 && depth == 0 {
				end = i
				break
			}
			if depth < 0 {
				return out
			}
		}
	}
	if start < 0 || end < 0 {
		return out
	}
	defs, err := splitSQLList(create[start+1 : end])
	if err != nil {
		return out
	}
	for _, def := range defs {
		p := typeParser{s: def}
		p.space()
		var name string
		if p.i < len(p.s) && (p.s[p.i] == '`' || p.s[p.i] == '"') {
			name, err = p.quoted(p.s[p.i])
			if err != nil {
				continue
			}
		} else {
			name = p.ident()
		}
		if name == "" {
			continue
		}
		if _, e := p.typ(0); e != nil {
			continue
		}
		attrsStart := p.i
		scan := sqlWhitespace(def)
		positions, e := sqlPositions(scan, ' ')
		if e != nil {
			continue
		}
		expressionStart := -1
		for _, pos := range positions {
			i := pos + 1
			if i < attrsStart {
				continue
			}
			for _, kind := range []string{"DEFAULT", "MATERIALIZED", "ALIAS", "EPHEMERAL"} {
				if keywordAt(scan, i, kind) && expressionStart < 0 {
					expressionStart = i + len(kind)
				}
			}
			if !keywordAt(scan, i, "TTL") {
				continue
			}
			if expressionStart >= 0 && !sqlExpressionComplete(def[expressionStart:i]) {
				continue
			}
			out[name] = strings.TrimSpace(def[i+3:])
			break
		}
	}
	return out
}
