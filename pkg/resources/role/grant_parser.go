package resourcerole

import (
	"fmt"
	"strings"
)

func keywordOutsideQuotes(text, keyword string) int {
	var quote byte
	for i := 0; i < len(text); i++ {
		if quote != 0 {
			if text[i] == '\\' {
				i++
				continue
			}
			if text[i] == quote {
				if i+1 < len(text) && text[i+1] == quote {
					i++
					continue
				}
				quote = 0
			}
			continue
		}
		if text[i] == '`' || text[i] == '"' || text[i] == '\'' {
			quote = text[i]
			continue
		}
		if i+len(keyword) <= len(text) && strings.EqualFold(text[i:i+len(keyword)], keyword) {
			return i
		}
	}
	return -1
}

func parseRoleIdentifier(text string) (string, error) {
	if text == "" {
		return "", fmt.Errorf("empty identifier")
	}
	quote := text[0]
	if quote != '`' && quote != '"' && quote != '\'' {
		for _, char := range text {
			if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_') {
				return "", fmt.Errorf("unsupported identifier %q", text)
			}
		}
		return text, nil
	}
	var result strings.Builder
	for i := 1; i < len(text); i++ {
		if text[i] == '\\' {
			i++
			if i >= len(text) {
				return "", fmt.Errorf("unterminated identifier %q", text)
			}
			switch text[i] {
			case 'n':
				result.WriteByte('\n')
			case 't':
				result.WriteByte('\t')
			case 'r':
				result.WriteByte('\r')
			case '0':
				result.WriteByte(0)
			default:
				result.WriteByte(text[i])
			}
		} else if text[i] == quote {
			if i+1 == len(text) {
				return result.String(), nil
			}
			if text[i+1] == quote {
				result.WriteByte(quote)
				i++
			} else {
				return "", fmt.Errorf("unsupported identifier suffix %q", text)
			}
		} else {
			result.WriteByte(text[i])
		}
	}
	return "", fmt.Errorf("unterminated identifier %q", text)
}
