package resourcetable

import "strings"

func equivalentReplicationPath(template, actual, database, table string) bool {
	path, ok := replicationPathLiteral(template)
	if !ok {
		return false
	}
	resolved, ok := replicationPathLiteral(actual)
	if !ok {
		return false
	}
	// match whole macro tokens, never a nested fragment of a custom macro
	for rest := path; strings.Contains(rest, "{"); {
		begin := strings.IndexByte(rest, '{')
		end := strings.IndexByte(rest[begin:], '}')
		if end < 0 || strings.Contains(rest[begin+1:begin+end], "{") {
			return false
		}
		rest = rest[begin+end+1:]
	}
	return strings.NewReplacer("{database}", database, "{table}", table).Replace(path) == resolved
}

// replicationPathLiteral accepts one complete literal and a conservative set of escapes
func replicationPathLiteral(sql string) (string, bool) {
	sql = strings.TrimSpace(sql)
	if len(sql) < 2 || sql[0] != '\'' || sql[len(sql)-1] != '\'' {
		return "", false
	}
	var value strings.Builder
	for i := 1; i < len(sql)-1; i++ {
		switch sql[i] {
		case '\'':
			if i+1 >= len(sql)-1 || sql[i+1] != '\'' {
				return "", false
			}
			value.WriteByte('\'')
			i++
		case '\\':
			i++
			if i >= len(sql)-1 {
				return "", false
			}
			var decoded byte
			switch sql[i] {
			case '\'', '\\':
				decoded = sql[i]
			case 'a':
				decoded = '\a'
			case 'b':
				decoded = '\b'
			case 'e':
				decoded = 0x1b
			case 'f':
				decoded = '\f'
			case 'n':
				decoded = '\n'
			case 'r':
				decoded = '\r'
			case 't':
				decoded = '\t'
			case 'v':
				decoded = '\v'
			case '0':
				decoded = 0
			case 'x':
				if i+2 >= len(sql)-1 {
					return "", false
				}
				hi, okHi := replicationPathHex(sql[i+1])
				lo, okLo := replicationPathHex(sql[i+2])
				if !okHi || !okLo {
					return "", false
				}
				decoded = hi<<4 | lo
				i += 2
			default:
				return "", false
			}
			value.WriteByte(decoded)
		default:
			value.WriteByte(sql[i])
		}
	}
	return value.String(), true
}

func replicationPathHex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
