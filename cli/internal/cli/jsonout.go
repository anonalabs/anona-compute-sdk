package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"unicode"
)

// Every --json object the CLI prints uses snake_case keys (sandbox_id,
// template_id, cpu_count, api_url), the convention the MCP server already
// follows. Objects built here are written that way; documents passed through
// from the gateway (usage, billing, preview links, build events) arrive in
// the E2B API's camelCase and are converted on the way out, so the rule holds
// for every command without each one remembering it.

// userDataKeys hold maps whose keys are the user's own (sandbox metadata,
// environment variables, log fields): their keys are data, not field names,
// and are never rewritten.
var userDataKeys = map[string]bool{"metadata": true, "fields": true, "envs": true, "env_vars": true}

// snakeKey converts a camelCase or PascalCase field name to snake_case:
// sandboxID -> sandbox_id, cpuCount -> cpu_count, memoryMB -> memory_mb,
// apiUrl -> api_url. Names already in snake_case are unchanged.
func snakeKey(s string) string {
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

// snakeValue rewrites every object key in a decoded JSON value.
func snakeValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			nk := snakeKey(k)
			if userDataKeys[nk] {
				out[nk] = val
				continue
			}
			out[nk] = snakeValue(val)
		}
		return out
	case []any:
		for i := range t {
			t[i] = snakeValue(t[i])
		}
		return t
	}
	return v
}

// snakeJSON marshals v with snake_case keys.
func snakeJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep integers and prices exactly as they were
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(snakeValue(doc)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
