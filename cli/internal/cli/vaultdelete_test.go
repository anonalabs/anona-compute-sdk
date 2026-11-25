package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A secret network rules name is refused with the gateway's words, and
// --remove-rules removes them and says which.
func TestVaultDeleteRefusedWhileRulesNameIt(t *testing.T) {
	deleted := 0
	mux := http.NewServeMux()
	mux.HandleFunc("DELETE /v1/vault/secrets/{name}", func(w http.ResponseWriter, r *http.Request) {
		js := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		switch q := r.URL.RawQuery; {
		case r.PathValue("name") == "unused" && q == "":
			deleted++
			w.WriteHeader(http.StatusNoContent)
		case q == "":
			js(409, map[string]any{"code": 409, "error_code": "secret_in_use",
				"message": `network rules use secret "ANTHROPIC": Authorization on github.com in the organization's network settings`})
		case q == "removeRules=true":
			deleted++
			js(200, map[string]any{"removedRules": []map[string]string{
				{"scope": "tenant_default", "scopeID": "t", "host": "github.com", "header": "Authorization"},
				{"scope": "template", "scopeID": "tpl-9", "host": "api.example.com", "header": "X-Key"}}})
		default:
			js(400, map[string]any{"code": 400, "message": "unexpected query " + q})
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e := newEnv(t)
	e.extra["ANONA_COMPUTE_API_URL"] = srv.URL

	if r := e.run("vault", "delete", "ANTHROPIC"); r.code == 0 || deleted != 0 || !strings.Contains(r.stderr, "Authorization on github.com") || r.stdout != "" {
		t.Fatalf("in use: code %d, %d deletes, stdout %q, stderr %q", r.code, deleted, r.stdout, r.stderr)
	}
	want := "deleted ANTHROPIC\nremoved rule: Authorization on github.com (network settings)\nremoved rule: X-Key on api.example.com (template tpl-9)\n"
	if r := e.must("vault", "delete", "ANTHROPIC", "--remove-rules"); r.stdout != want {
		t.Fatalf("with rules: %q", r.stdout)
	}
	var got struct {
		Deleted      bool `json:"deleted"`
		RemovedRules []struct {
			ScopeID string `json:"scope_id"`
			Host    string `json:"host"`
		} `json:"removed_rules"`
	}
	r := e.must("vault", "rm", "ANTHROPIC", "--remove-rules", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &got); err != nil || !got.Deleted || len(got.RemovedRules) != 2 || got.RemovedRules[1].ScopeID != "tpl-9" {
		t.Fatalf("json: %v %s", err, r.stdout)
	}
	if r := e.must("vault", "delete", "unused"); r.stdout != "deleted unused\n" || deleted != 3 {
		t.Fatalf("unused: %q", r.stdout)
	}
}
