package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDashboard serves the routes `anona-compute login` uses: the Better Auth
// device-authorization endpoints and /api/cli/token|logout. The real
// dashboard is covered by a browser end-to-end test kept with the dashboard.
type fakeDashboard struct {
	mu       sync.Mutex
	polls    int
	pending  int    // authorization_pending answers before the token
	deny     bool   // answer access_denied
	orgs     int    // organizations of the user (2+: org_required without --org)
	role     string // role in the picked org
	revoked  bool
	jwt      string
	lastOrg  string
	gotUA    string
	gotForms []map[string]string
}

func (d *fakeDashboard) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /api/auth/device/code", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["client_id"] != "anona-compute" {
			write(w, 400, map[string]string{"error": "invalid_client"})
			return
		}
		write(w, 200, map[string]any{"device_code": "dev-code-1", "user_code": "ABCDEFGH", "verification_uri": "/device",
			"verification_uri_complete": "/device?user_code=ABCDEFGH", "expires_in": 60, "interval": 1})
	})
	mux.HandleFunc("POST /api/auth/device/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		d.mu.Lock()
		defer d.mu.Unlock()
		d.gotForms = append(d.gotForms, body)
		d.gotUA = r.Header.Get("User-Agent")
		d.polls++
		switch {
		case body["grant_type"] != "urn:ietf:params:oauth:grant-type:device_code" || body["device_code"] != "dev-code-1":
			write(w, 400, map[string]string{"error": "invalid_grant"})
		case d.polls <= d.pending:
			write(w, 400, map[string]string{"error": "authorization_pending", "error_description": "Authorization pending"})
		case d.deny:
			write(w, 400, map[string]string{"error": "access_denied", "error_description": "Access denied"})
		default:
			write(w, 200, map[string]any{"access_token": "session-secret-1", "token_type": "Bearer", "expires_in": 604800})
		}
	})
	mux.HandleFunc("POST /api/cli/token", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer session-secret-1" || d.revoked {
			write(w, 401, map[string]string{"error": "invalid_session"})
			return
		}
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		org := body["org"]
		if org == "" {
			org = d.lastOrg
		}
		if org == "" && d.orgs > 1 {
			write(w, 409, map[string]any{"error": "org_required", "orgs": []map[string]string{{"slug": "acme", "name": "Acme", "role": "owner"}, {"slug": "beta", "name": "Beta", "role": "member"}}})
			return
		}
		if org == "" {
			org = "acme"
		}
		d.lastOrg = org
		d.jwt = "jwt-for-" + org
		write(w, 200, map[string]any{"token": d.jwt, "tokenType": "Bearer", "expiresAt": time.Now().Add(15 * time.Minute).UTC(),
			"user": map[string]string{"id": "u1", "email": "dev@example.test", "name": "Dev"},
			"org":  map[string]string{"id": "o-" + org, "slug": org, "name": strings.ToUpper(org[:1]) + org[1:]}, "role": d.role})
	})
	mux.HandleFunc("POST /api/cli/logout", func(w http.ResponseWriter, r *http.Request) {
		d.mu.Lock()
		defer d.mu.Unlock()
		was := !d.revoked && r.Header.Get("Authorization") == "Bearer session-secret-1"
		d.revoked = true
		write(w, 200, map[string]bool{"revoked": was})
	})
	return mux
}

func loginEnv(t *testing.T, d *fakeDashboard) *env {
	e := newEnv(t)
	hs := httptest.NewServer(d.handler(t))
	t.Cleanup(hs.Close)
	e.srv.SetBearers(func(tok string) bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return !d.revoked && tok == d.jwt
	})
	e.extra["ANONA_COMPUTE_API_KEY"] = ""
	e.extra["ANONA_COMPUTE_WEB_URL"] = hs.URL
	e.extra["ANONA_COMPUTE_CREDENTIAL_STORE"] = "file"
	return e
}

func TestDeviceLoginWhoamiLogout(t *testing.T) {
	d := &fakeDashboard{pending: 2, role: "member"}
	e := loginEnv(t, d)
	r := e.run("login", "--no-browser")
	if r.code != 0 {
		t.Fatalf("login: exit %d\n%s", r.code, r.stderr)
	}
	t.Logf("login stderr:\n%s", r.stderr)
	if !strings.Contains(r.stderr, "/device?user_code=ABCDEFGH") || !strings.Contains(r.stderr, "Code: ABCD-EFGH") ||
		!strings.Contains(r.stderr, "Signed in as dev@example.test in organization Acme (acme) with role member") {
		t.Fatalf("login output: %s", r.stderr)
	}
	if d.polls != 3 {
		t.Fatalf("polls = %d, want 2 pending + 1", d.polls)
	}
	host, _ := os.Hostname()
	if !strings.Contains(d.gotUA, "CLI on "+host) {
		t.Fatalf("user agent %q does not name the host", d.gotUA)
	}

	// The secret is in the 0600 credentials file, not in config.toml.
	cred := filepath.Join(e.cfgDir, "anona-compute", "credentials.json")
	st, err := os.Stat(cred)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("credentials file: %v %v", err, st)
	}
	cfg, _ := os.ReadFile(filepath.Join(e.cfgDir, "anona-compute", "config.toml"))
	if strings.Contains(string(cfg), "session-secret-1") || !strings.Contains(string(cfg), `auth = 'session'`) {
		t.Fatalf("config.toml:\n%s", cfg)
	}

	who := jsonLines(t, e.must("whoami", "--json").stdout)
	if who[0]["email"] != "dev@example.test" || who[0]["org"] != "acme" || who[0]["role"] != "member" || who[0]["auth"] != "session" {
		t.Fatalf("whoami: %v", who)
	}
	e.must("sandbox", "create", "-t", "base")
	e.must("list")
	// The gateway saw the JWT as a bearer and no API key.
	for _, req := range e.srv.Requests() {
		if req.Header.Get("Authorization") != "Bearer jwt-for-acme" || req.Header.Get("X-API-Key") != "" {
			t.Fatalf("%s %s sent Authorization %q X-API-Key %q", req.Method, req.Path, req.Header.Get("Authorization"), req.Header.Get("X-API-Key"))
		}
	}
	// An API key in the environment wins over the login.
	e.extra["ANONA_COMPUTE_API_KEY"] = testKey
	if w := e.must("whoami"); !strings.Contains(w.stdout, "API key") {
		t.Fatalf("whoami with ANONA_COMPUTE_API_KEY: %s", w.stdout)
	}
	e.extra["ANONA_COMPUTE_API_KEY"] = ""

	out := jsonLines(t, e.must("logout", "--json").stdout)
	if out[0]["status"] != "revoked" || !d.revoked {
		t.Fatalf("logout: %v revoked=%v", out, d.revoked)
	}
	if _, err := os.Stat(cred); !os.IsNotExist(err) {
		t.Fatalf("credentials file still there: %v", err)
	}
	if r := e.run("list"); r.code != 3 {
		t.Fatalf("list after logout: exit %d %s", r.code, r.stderr)
	}
}

func TestDeviceLoginRevokedSession(t *testing.T) {
	d := &fakeDashboard{role: "owner"}
	e := loginEnv(t, d)
	e.must("login", "--no-browser")
	d.mu.Lock()
	d.revoked = true // revoked elsewhere (dashboard, another logout)
	d.mu.Unlock()
	r := e.run("list")
	if r.code != 3 || !strings.Contains(r.stderr, "revoked or has expired") {
		t.Fatalf("list with a revoked session: exit %d %s", r.code, r.stderr)
	}
	out := jsonLines(t, e.must("logout", "--json").stdout)
	if out[0]["status"] != "already_revoked" {
		t.Fatalf("logout: %v", out)
	}
}

func TestDeviceLoginDenied(t *testing.T) {
	d := &fakeDashboard{deny: true}
	e := loginEnv(t, d)
	r := e.run("login", "--no-browser")
	if r.code != 3 || !strings.Contains(r.stderr, "denied") {
		t.Fatalf("denied login: exit %d %s", r.code, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(e.cfgDir, "anona-compute", "credentials.json")); !os.IsNotExist(err) {
		t.Fatalf("credentials saved after a denial")
	}
}

func TestDeviceLoginOrgChoice(t *testing.T) {
	d := &fakeDashboard{orgs: 2, role: "owner"}
	e := loginEnv(t, d)
	// No organization picked and no terminal to ask on: a usage error naming them,
	// and the unusable session is revoked.
	r := e.run("login", "--no-browser")
	if r.code != 2 || !strings.Contains(r.stderr, "acme, beta") || !d.revoked {
		t.Fatalf("login without --org: exit %d revoked=%v %s", r.code, d.revoked, r.stderr)
	}
	d2 := &fakeDashboard{orgs: 2, role: "member"}
	e2 := loginEnv(t, d2)
	lines := jsonLines(t, e2.must("login", "--no-browser", "--org", "beta", "--json").stdout)
	if lines[0]["type"] != "device_code" || lines[1]["org"] != "beta" {
		t.Fatalf("login --org beta: %v", lines)
	}
	e2.must("list")
}
