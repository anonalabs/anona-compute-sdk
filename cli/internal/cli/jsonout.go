package cli

import (
	"bytes"
	"encoding/json"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/jsoncase"
)

// Every --json object the CLI prints uses snake_case keys (sandbox_id,
// template_id, cpu_count, api_url), the convention the MCP server already
// follows. Objects built here are written that way; documents passed through
// from the gateway (usage, billing, preview links, build events) arrive in
// the E2B API's camelCase and are converted on the way out, so the rule holds
// for every command without each one remembering it.

// snakeKey and snakeValue are jsoncase's, shared with the MCP server.
func snakeKey(s string) string { return jsoncase.Key(s) }

func snakeValue(v any) any { return jsoncase.Value(v) }

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
