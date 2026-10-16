package resourcetable

import (
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	hashicorpcty "github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
)

// parses a complete ClickHouse type without accepting SQL expressions
func CanonicalType(value string) (string, error) {
	if len(value) > 65536 {
		return "", fmt.Errorf("type is too long")
	}
	p := typeParser{s: value}
	result, err := p.typ(0)
	if err != nil {
		return "", err
	}
	p.space()
	if p.i != len(p.s) {
		return "", p.err("unexpected trailing input")
	}
	return result, nil
}

func ValidateDataType(value any, _ hashicorpcty.Path) diag.Diagnostics {
	s, ok := value.(string)
	if !ok {
		return diag.Errorf("type must be a string")
	}
	if _, err := CanonicalType(s); err != nil {
		return diag.Errorf("invalid ClickHouse type: %s", err)
	}
	return nil
}

type typeParser struct {
	s string
	i int
}

func (p *typeParser) err(message string) error { return fmt.Errorf("%s at byte %d", message, p.i) }
func (p *typeParser) space() {
	for p.i < len(p.s) && (p.s[p.i] == ' ' || p.s[p.i] == '\t' || p.s[p.i] == '\r' || p.s[p.i] == '\n') {
		p.i++
	}
}
func (p *typeParser) take(c byte) bool {
	p.space()
	if p.i < len(p.s) && p.s[p.i] == c {
		p.i++
		return true
	}
	return false
}
func (p *typeParser) ident() string {
	p.space()
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || p.i > start && c >= '0' && c <= '9') {
			break
		}
		p.i++
	}
	return p.s[start:p.i]
}
func (p *typeParser) number(signed bool) (int64, error) {
	p.space()
	start := p.i
	if signed && p.i < len(p.s) && (p.s[p.i] == '-' || p.s[p.i] == '+') {
		p.i++
	}
	digits := p.i
	for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
		p.i++
	}
	if digits == p.i {
		return 0, p.err("expected integer")
	}
	n, err := strconv.ParseInt(p.s[start:p.i], 10, 64)
	if err != nil {
		return 0, p.err("integer out of range")
	}
	return n, nil
}
func (p *typeParser) quoted(delim byte) (string, error) {
	p.space()
	if p.i >= len(p.s) || p.s[p.i] != delim {
		return "", p.err("expected quoted string")
	}
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		p.i++
		if c == delim {
			if p.i < len(p.s) && p.s[p.i] == delim {
				p.i++
				b.WriteByte(c)
				continue
			}
			return b.String(), nil
		}
		if c == '\\' {
			if p.i == len(p.s) {
				break
			}
			c = p.s[p.i]
			p.i++
			switch c {
			case 'n':
				c = '\n'
			case 'r':
				c = '\r'
			case 't':
				c = '\t'
			case '0':
				c = 0
			case '\\', '\'', '`':
			default:
				return "", p.err("unsupported escape")
			}
		}
		if c == 0 {
			return "", p.err("NUL is not allowed")
		}
		b.WriteByte(c)
	}
	return "", p.err("unterminated quoted string")
}
func quoteType(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\t", "\\t")
	return "'" + s + "'"
}

var aggregateNumberPattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)

func (p *typeParser) aggregateFunction() (string, error) {
	name := p.ident()
	if name == "" {
		return "", p.err("expected aggregate function name")
	}
	if !p.take('(') {
		return name, nil
	}
	var params []string
	if p.take(')') {
		return name + "()", nil
	}
	for {
		p.space()
		var value string
		if p.i < len(p.s) && p.s[p.i] == '\'' {
			s, err := p.quoted('\'')
			if err != nil {
				return "", err
			}
			value = quoteType(s)
		} else {
			start := p.i
			for p.i < len(p.s) && strings.ContainsRune("0123456789+-.eE", rune(p.s[p.i])) {
				p.i++
			}
			raw := p.s[start:p.i]
			if !aggregateNumberPattern.MatchString(raw) {
				return "", p.err("expected numeric or string aggregate parameter")
			}
			if !strings.ContainsAny(raw, ".eE") {
				n, ok := new(big.Int).SetString(raw, 10)
				if !ok {
					return "", p.err("invalid aggregate integer")
				}
				value = n.String()
			} else {
				n, err := strconv.ParseFloat(raw, 64)
				if err != nil || math.IsInf(n, 0) || math.IsNaN(n) {
					return "", p.err("aggregate number out of range")
				}
				if n == 0 {
					n = 0
				}
				value = strconv.FormatFloat(n, 'g', -1, 64)
			}
		}
		params = append(params, value)
		if !p.take(',') {
			break
		}
	}
	if !p.take(')') {
		return "", p.err("expected closing aggregate parameter parenthesis")
	}
	return name + "(" + strings.Join(params, ", ") + ")", nil
}

func (p *typeParser) typ(depth int) (string, error) {
	if depth > 64 {
		return "", p.err("type nesting is too deep")
	}
	name := p.ident()
	if name == "" {
		return "", p.err("expected type name")
	}
	simple := " UInt8 UInt16 UInt32 UInt64 UInt128 UInt256 Int8 Int16 Int32 Int64 Int128 Int256 Float32 Float64 String Bool UUID IPv4 IPv6 Date Date32 JSON Nothing "
	if strings.Contains(simple, " "+name+" ") {
		return name, nil
	}
	args := []string{}
	switch name {
	case "DateTime":
		if !p.take('(') {
			return name, nil
		}
		s, e := p.quoted('\'')
		if e != nil {
			return "", e
		}
		if s == "" {
			return "", p.err("empty timezone")
		}
		args = append(args, quoteType(s))
	case "DateTime64", "FixedString", "Decimal", "Decimal32", "Decimal64", "Decimal128", "Decimal256":
		if !p.take('(') {
			if name == "DateTime64" {
				return "DateTime64(3)", nil
			}
			return "", p.err("expected type arguments")
		}
		n, e := p.number(false)
		if e != nil {
			return "", e
		}
		max := int64(0)
		min := int64(0)
		switch name {
		case "DateTime64":
			max = 9
		case "FixedString":
			min = 1
			max = 1 << 30
		case "Decimal":
			min = 1
			max = 76
		case "Decimal32":
			max = 9
		case "Decimal64":
			max = 18
		case "Decimal128":
			max = 38
		case "Decimal256":
			max = 76
		}
		if n < min || n > max {
			return "", p.err("numeric type argument out of range")
		}
		args = append(args, strconv.FormatInt(n, 10))
		if name == "Decimal" {
			if !p.take(',') {
				return "", p.err("expected decimal scale")
			}
			scale, e := p.number(false)
			if e != nil {
				return "", e
			}
			if scale > n {
				return "", p.err("scale exceeds precision")
			}
			args = append(args, strconv.FormatInt(scale, 10))
		}
		if name == "DateTime64" && p.take(',') {
			s, e := p.quoted('\'')
			if e != nil {
				return "", e
			}
			if s == "" {
				return "", p.err("empty timezone")
			}
			args = append(args, quoteType(s))
		}
		if strings.HasPrefix(name, "Decimal") && name != "Decimal" {
			name = "Decimal"
			args = []string{strconv.FormatInt(max, 10), strconv.FormatInt(n, 10)}
		}
	case "AggregateFunction", "SimpleAggregateFunction":
		if !p.take('(') {
			return "", p.err("expected aggregate type arguments")
		}
		function, err := p.aggregateFunction()
		if err != nil {
			return "", err
		}
		args = append(args, function)
		for p.take(',') {
			arg, err := p.typ(depth + 1)
			if err != nil {
				return "", err
			}
			args = append(args, arg)
		}
		if name == "SimpleAggregateFunction" && len(args) < 2 {
			return "", p.err("expected aggregate input type")
		}
	case "Array", "Nullable", "LowCardinality", "Map":
		if !p.take('(') {
			return "", p.err("expected type arguments")
		}
		count := 1
		if name == "Map" {
			count = 2
		}
		for i := 0; i < count; i++ {
			if i > 0 && !p.take(',') {
				return "", p.err("expected comma")
			}
			s, e := p.typ(depth + 1)
			if e != nil {
				return "", e
			}
			inner := strings.SplitN(s, "(", 2)[0]
			if name == "Nullable" && (inner == "Nullable" || inner == "Array" || inner == "Map" || inner == "Tuple" || inner == "Nested") {
				return "", p.err("type cannot be wrapped in Nullable")
			}
			args = append(args, s)
		}
	case "Tuple", "Nested":
		if !p.take('(') {
			return "", p.err("expected type arguments")
		}
		named := -1
		seen := map[string]bool{}
		if name == "Tuple" && p.take(')') {
			return "Tuple()", nil
		}
		for {
			start := p.i
			p.space()
			field := ""
			quoted := p.i < len(p.s) && p.s[p.i] == '`'
			if quoted {
				var e error
				field, e = p.quoted('`')
				if e != nil {
					return "", e
				}
			} else {
				field = p.ident()
			}
			p.space()
			isNamed := quoted || p.i < len(p.s) && (unicode.IsLetter(rune(p.s[p.i])) || p.s[p.i] == '_')
			mode := 0
			if isNamed {
				mode = 1
			}
			if named < 0 {
				named = mode
			}
			if named != mode || name == "Nested" && !isNamed {
				return "", p.err("inconsistent named fields")
			}
			prefix := ""
			if isNamed {
				if field == "" || seen[field] {
					return "", p.err("empty or duplicate field name")
				}
				seen[field] = true
				prefix = "`" + strings.ReplaceAll(strings.ReplaceAll(field, "\\", "\\\\"), "`", "\\`") + "` "
			} else {
				p.i = start
			}
			s, e := p.typ(depth + 1)
			if e != nil {
				return "", e
			}
			args = append(args, prefix+s)
			if !p.take(',') {
				break
			}
		}
	case "Enum8", "Enum16":
		if !p.take('(') {
			return "", p.err("expected enum values")
		}
		labels := map[string]bool{}
		values := map[int64]bool{}
		for {
			s, e := p.quoted('\'')
			if e != nil {
				return "", e
			}
			if !p.take('=') {
				return "", p.err("expected equals")
			}
			n, e := p.number(true)
			if e != nil {
				return "", e
			}
			limit := int64(128)
			if name == "Enum16" {
				limit = 32768
			}
			if n < -limit || n >= limit || labels[s] || values[n] {
				return "", p.err("invalid or duplicate enum value")
			}
			labels[s] = true
			values[n] = true
			args = append(args, quoteType(s)+" = "+strconv.FormatInt(n, 10))
			if !p.take(',') {
				break
			}
		}
	default:
		return "", p.err("unsupported type " + name)
	}
	if !p.take(')') {
		return "", p.err("expected closing parenthesis")
	}
	return name + "(" + strings.Join(args, ", ") + ")", nil
}
