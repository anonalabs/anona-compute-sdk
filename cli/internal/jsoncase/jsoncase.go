// Package jsoncase rewrites JSON object keys to snake_case, the convention of
// every --json object the CLI prints and every MCP tool result. Documents the
// gateway returns in the E2B API's camelCase are converted on the way out.
package jsoncase

import (
	"strings"
	"unicode"
)

// userDataKeys hold maps whose keys are the user's own (sandbox metadata,
// environment variables, log fields): their keys are data, not field names,
// and are never rewritten.
var userDataKeys = map[string]bool{"metadata": true, "fields": true, "envs": true, "env_vars": true}

// Key converts a camelCase or PascalCase field name to snake_case:
// sandboxID -> sandbox_id, cpuCount -> cpu_count, memoryMB -> memory_mb,
// apiUrl -> api_url. Names already in snake_case are unchanged.
func Key(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		if unicode.IsUpper(r) {
			if i > 0 {
				prev := rs[i-1]
				nextLower := i+1 < len(rs) && unicode.IsLower(rs[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					b.WriteByte('_')
				}
			}
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Value rewrites every object key in a decoded JSON value.
func Value(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			nk := Key(k)
			if userDataKeys[nk] {
				out[nk] = val
				continue
			}
			out[nk] = Value(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = Value(t[i])
		}
		return t
	}
	return v
}
