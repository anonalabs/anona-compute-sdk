package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The list shows what was revoked and how to bring it back; the reconnect
// sends one request and shows the gateway's refusal as it is.
func TestVaultReconnect(t *testing.T) {
	const id = "vlt_aaaaaaaaaaaaaaaaaaaaaaaa"
	const refusal = "an API key cannot reconnect a vault secret; an organization owner or admin does that, in the console or signed in with the CLI"
	refuse := true
	var calls []string
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/vault/bindings/{id}/reconnect", func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.PathValue("id"))
		if refuse {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(403)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 403, "message": refusal})
			return
		}
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /v1/vault/secrets", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"enabled":true,"secrets":[{"name":"GH","version":1,"allowedHosts":["api.github.com"],"bindings":[],"revoked":[` +
			`{"id":"` + id + `","secret":"GH","scope":"tenant_default","scopeID":"t","host":"api.github.com","header":"Authorization","format":"Bearer ${SECRET}","revokedAt":"2026-11-25T10:00:00Z"}]}]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e := newEnv(t)
	e.extra["ANONA_COMPUTE_API_URL"] = srv.URL

	if r := e.run("vault", "reconnect", id); r.code == 0 || !strings.Contains(r.stderr, refusal) || r.stdout != "" {
		t.Fatalf("refused: code %d, stdout %q, stderr %q", r.code, r.stdout, r.stderr)
	}
	refuse = false
	if r := e.must("vault", "reconnect", id); r.stdout != "reconnected "+id+"\n" {
		t.Fatalf("reconnect: %q", r.stdout)
	}
	if r := e.must("vault", "reconnect", id, "--json"); !strings.Contains(r.stdout, `"reconnected"`) {
		t.Fatalf("json: %q", r.stdout)
	}
	if len(calls) != 3 || calls[0] != id {
		t.Fatalf("calls %v", calls)
	}
	if r := e.must("vault", "list"); !strings.Contains(r.stdout, "GH revoked") || !strings.Contains(r.stdout, id) || strings.Contains(r.stdout, "GH bindings") {
		t.Fatalf("list: %q", r.stdout)
	}
	var l struct {
		Secrets []struct {
			Revoked []struct {
				ID        string `json:"id"`
				RevokedAt string `json:"revoked_at"`
			} `json:"revoked"`
		} `json:"secrets"`
	}
	r := e.must("vault", "list", "--json")
	if err := json.Unmarshal([]byte(r.stdout), &l); err != nil || len(l.Secrets) != 1 || len(l.Secrets[0].Revoked) != 1 ||
		l.Secrets[0].Revoked[0].ID != id || l.Secrets[0].Revoked[0].RevokedAt != "2026-11-25T10:00:00Z" {
		t.Fatalf("json list: %v %s", err, r.stdout)
	}
}
