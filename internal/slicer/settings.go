package slicer

import "strings"

// splitSets preserves JSON values while retaining Kong's batch and escaped-comma behavior.
func splitSets(values []string) []string {
	var out []string
	for _, value := range values {
		var token strings.Builder
		depth := 0
		quoted, escaped := false, false
		for i, ch := range value {
			switch {
			case escaped:
				if ch != ',' {
					token.WriteRune('\\')
				}
				token.WriteRune(ch)
				escaped = false
				continue
			case ch == '\\' && i < len(value)-1:
				escaped = true
				continue
			}
			if quoted {
				token.WriteRune(ch)
				if ch == '"' {
					quoted = false
				}
				continue
			}
			switch ch {
			case '"':
				quoted = depth > 0
			case '[':
				if depth > 0 {
					depth++
				} else if _, prefix, ok := strings.Cut(token.String(), "="); ok && strings.TrimSpace(prefix) == "" {
					depth = 1 // a JSON list starts at the beginning of the setting value
				}
			case '{':
				if depth > 0 {
					depth++
				}
			case ']', '}':
				if depth > 0 {
					depth--
				}
			case ',':
				if depth == 0 {
					out = append(out, token.String())
					token.Reset()
					continue
				}
			}
			token.WriteRune(ch)
		}
		if token.Len() > 0 {
			out = append(out, token.String())
		}
	}
	return out
}
