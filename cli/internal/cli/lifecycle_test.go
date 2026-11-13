package cli_test

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/fakee2b"
)

func lastRequest(t *testing.T, e *env, method, pathSuffix string) map[string]any {
	t.Helper()
	reqs := e.srv.Requests()
	for i := len(reqs) - 1; i >= 0; i-- {
		r := reqs[i]
		if r.Method == method && strings.HasSuffix(r.Path, pathSuffix) {
			if r.Header.Get("X-API-Key") != testKey {
				t.Fatalf("%s %s: X-API-Key %q", method, r.Path, r.Header.Get("X-API-Key"))
			}
			m := map[string]any{"_path": r.Path, "_query": r.Query}
			if r.Body != "" {
				if err := json.Unmarshal([]byte(r.Body), &m); err != nil {
					t.Fatalf("body %q: %v", r.Body, err)
				}
				m["_path"], m["_query"] = r.Path, r.Query
			}
			return m
		}
	}
	t.Fatalf("no %s *%s request", method, pathSuffix)
	return nil
}

// create, list, info, pause, list --state paused, resume, timeout,
// fork --count 2, kill, with -o json parsed line by line.
func TestLifecycle(t *testing.T) {
	e := newEnv(t)

	r := e.must("create", "-t", "base", "--timeout", "10m", "-e", "A=1", "--metadata", "team=x", "--no-internet", "--json")
	lines := jsonLines(t, r.stdout)
	if len(lines) != 1 {
		t.Fatalf("create: %q", r.stdout)
	}
	id := lines[0]["sandbox_id"].(string)
	if _, leaked := lines[0]["envd_access_token"]; leaked || strings.Contains(r.stdout, "envd-token") {
		t.Fatalf("create printed the access token: %q", r.stdout)
	}
	body := lastRequest(t, e, "POST", "/v2/sandboxes")
	if body["_path"] != "/v2/sandboxes" || body["templateID"] != "base" || body["timeout"] != float64(600) ||
		body["allow_internet_access"] != false || body["envVars"].(map[string]any)["A"] != "1" ||
		body["metadata"].(map[string]any)["team"] != "x" {
		t.Fatalf("create body: %v", body)
	}

	// Text create with --count prints one ID per line.
	r = e.must("create", "--count", "2")
	ids := strings.Fields(r.stdout)
	if len(ids) != 2 {
		t.Fatalf("create --count 2: %q", r.stdout)
	}

	r = e.must("list", "--json")
	if got := len(jsonList(t, r.stdout)); got != 3 {
		t.Fatalf("list: %d lines: %q", got, r.stdout)
	}
	q := lastRequest(t, e, "GET", "/v2/sandboxes")["_query"]
	if q.(url.Values)["state"][0] != "running" {
		t.Fatalf("list query %v", q)
	}
	r = e.must("list")
	if !strings.Contains(r.stdout, "SANDBOX ID") || !strings.Contains(r.stdout, id) {
		t.Fatalf("list text: %q", r.stdout)
	}
	r = e.must("list", "--metadata", "team=x", "--json")
	if got := jsonList(t, r.stdout); len(got) != 1 || got[0]["sandbox_id"] != id {
		t.Fatalf("list --metadata: %q", r.stdout)
	}
	// Pagination: --limit 2 of 3, then the cursor is followed for all.
	if got := jsonList(t, e.must("list", "--limit", "2", "--json").stdout); len(got) != 2 {
		t.Fatalf("list --limit 2: %d", len(got))
	}

	r = e.must("info", id, "-o", "json")
	info := jsonLines(t, r.stdout)[0]
	if info["state"] != "running" || info["cpu_count"] != float64(2) || info["memory_mb"] != float64(512) {
		t.Fatalf("info: %v", info)
	}
	if strings.Contains(r.stdout, "envd-token") {
		t.Fatal("info printed the access token")
	}

	r = e.must("pause", id, "--json")
	if jsonLines(t, r.stdout)[0]["status"] != "paused" {
		t.Fatalf("pause: %q", r.stdout)
	}
	if b := lastRequest(t, e, "POST", "/pause"); b["memory"] != true {
		t.Fatalf("pause body %v", b)
	}
	r = e.must("pause", id, "--json") // 409 → already paused, exit 0
	if jsonLines(t, r.stdout)[0]["status"] != "already_paused" {
		t.Fatalf("pause again: %q", r.stdout)
	}
	r = e.must("list", "--state", "paused", "--json")
	if got := jsonList(t, r.stdout); len(got) != 1 || got[0]["sandbox_id"] != id || got[0]["state"] != "paused" {
		t.Fatalf("list paused: %q", r.stdout)
	}
	if got := jsonList(t, e.must("list", "--state", "all", "--json").stdout); len(got) != 3 {
		t.Fatalf("list all: %d", len(got))
	}
	if q := lastRequest(t, e, "GET", "/v2/sandboxes")["_query"].(url.Values); q["state"][0] != "running,paused" {
		t.Fatalf("state=all query %v", q)
	}

	r = e.must("resume", id, "--json")
	if jsonLines(t, r.stdout)[0]["status"] != "resumed" {
		t.Fatalf("resume: %q", r.stdout)
	}
	if b := lastRequest(t, e, "POST", "/connect"); b["_path"] != "/v2/sandboxes/"+id+"/connect" || b["timeout"] != float64(300) {
		t.Fatalf("resume request %v", b)
	}
	r = e.must("resume", id, "--json")
	if jsonLines(t, r.stdout)[0]["status"] != "running" {
		t.Fatalf("resume running: %q", r.stdout)
	}

	r = e.must("timeout", id, "1h", "--json")
	if jsonLines(t, r.stdout)[0]["timeout"] != float64(3600) {
		t.Fatalf("timeout: %q", r.stdout)
	}
	if sb, _ := e.srv.Sandbox(id); sb.LastTimeout != 3600 {
		t.Fatalf("server timeout %d", sb.LastTimeout)
	}
	e.must("extend", id, "120")
	if sb, _ := e.srv.Sandbox(id); sb.LastTimeout != 120 {
		t.Fatalf("extend: server timeout %d", sb.LastTimeout)
	}

	r = e.must("fork", id, "--count", "2", "--json")
	forks := jsonLines(t, r.stdout)
	if len(forks) != 2 || forks[0]["sandbox_id"] == forks[1]["sandbox_id"] {
		t.Fatalf("fork: %q", r.stdout)
	}
	if b := lastRequest(t, e, "POST", "/fork"); b["count"] != float64(2) || b["timeout"] != float64(300) {
		t.Fatalf("fork body %v", b)
	}
	for _, f := range forks {
		sb, ok := e.srv.Sandbox(f["sandbox_id"].(string))
		if !ok || sb.ForkedFrom != id {
			t.Fatalf("fork %v not recorded", f)
		}
	}

	r = e.must("kill", id, "--json")
	if got := jsonLines(t, r.stdout); got[0]["status"] != "killed" {
		t.Fatalf("kill: %q", r.stdout)
	}
	if _, ok := e.srv.Sandbox(id); ok {
		t.Fatal("sandbox still exists after kill")
	}
	// Not found → exit 4, JSON error on stderr.
	r = e.run("info", id, "--json")
	if r.code != 4 {
		t.Fatalf("info after kill: exit %d", r.code)
	}
	if errLine := jsonLines(t, r.stderr); errLine[0]["error"].(map[string]any)["status"] != float64(404) {
		t.Fatalf("error json: %q", r.stderr)
	}
	if r := e.run("kill", id); r.code != 4 {
		t.Fatalf("kill missing: exit %d", r.code)
	}

	r = e.must("kill", "--all", "--yes", "--json")
	if got := jsonLines(t, r.stdout); len(got) != 4 {
		t.Fatalf("kill --all: %q", r.stdout)
	}
	if got := jsonList(t, e.must("list", "--state", "all", "--json").stdout); len(got) != 0 {
		t.Fatalf("after kill --all: %d", len(got))
	}
}

func TestListPagination(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 230; i++ {
		e.srv.AddSandbox("base")
	}
	r := e.must("list", "--json")
	if got := len(jsonList(t, r.stdout)); got != 230 {
		t.Fatalf("list: %d, want 230", got)
	}
	pages := 0
	for _, req := range e.srv.Requests() {
		if req.Path == "/v2/sandboxes" && req.Method == "GET" {
			pages++
		}
	}
	if pages != 3 {
		t.Fatalf("pages fetched: %d, want 3", pages)
	}
}

func TestAuthErrors(t *testing.T) {
	e := newEnv(t)
	e.extra["ANONA_COMPUTE_API_KEY"] = "e2b_ffff"
	if r := e.run("list"); r.code != 3 || !strings.Contains(r.stderr, "Invalid API key") {
		t.Fatalf("wrong key: exit %d %q", r.code, r.stderr)
	}
	e.extra["ANONA_COMPUTE_API_KEY"] = ""
	if r := e.run("list"); r.code != 3 {
		t.Fatalf("no key: exit %d %q", r.code, r.stderr)
	}
	e.extra["ANONA_COMPUTE_API_KEY"] = "not-a-key"
	if r := e.run("list"); r.code != 3 {
		t.Fatalf("bad format: exit %d %q", r.code, r.stderr)
	}
	// E2B_API_KEY works as a fallback.
	e.extra["ANONA_COMPUTE_API_KEY"] = ""
	e.extra["E2B_API_KEY"] = testKey
	e.must("list")
}

// The verb people read is "delete"; "kill" and "rm" keep working for
// scripts, and the machine-readable result is the same whichever name ran.
func TestDeleteIsTheVerbAndKillStillWorks(t *testing.T) {
	e := newEnv(t)
	for _, verb := range []string{"delete", "kill", "rm"} {
		id := jsonLines(t, e.must("create", "-t", "base", "--json").stdout)[0]["sandbox_id"].(string)
		r := e.must(verb, id)
		if r.stderr != "Deleted "+id+"\n" {
			t.Errorf("%s: stderr %q, want %q", verb, r.stderr, "Deleted "+id+"\n")
		}
		if strings.Contains(r.stderr, "Kill") {
			t.Errorf("%s: human output still says Kill: %q", verb, r.stderr)
		}
		if _, ok := e.srv.Sandbox(id); ok {
			t.Errorf("%s: sandbox still exists", verb)
		}
		id = jsonLines(t, e.must("create", "-t", "base", "--json").stdout)[0]["sandbox_id"].(string)
		if got := jsonLines(t, e.must(verb, id, "--json").stdout); got[0]["status"] != "killed" {
			t.Errorf("%s --json: %v, want status killed (machine output is unchanged)", verb, got)
		}
	}
	// The same command under sandbox.
	id := jsonLines(t, e.must("create", "-t", "base", "--json").stdout)[0]["sandbox_id"].(string)
	if r := e.must("sandbox", "delete", id); r.stderr != "Deleted "+id+"\n" {
		t.Errorf("sandbox delete: %q", r.stderr)
	}
	// Help leads with delete and still lists kill as an alias.
	h := e.must("kill", "--help").stdout
	if !strings.Contains(h, "anona-compute delete SANDBOX_ID...") || !strings.Contains(h, "delete, kill, rm") || strings.Contains(h, "Kill sandboxes") {
		t.Errorf("kill --help:\n%s", h)
	}
}

func countRequests(e *env, method, path string) int {
	n := 0
	for _, r := range e.srv.Requests() {
		if r.Method == method && r.Path == path {
			n++
		}
	}
	return n
}

// The decision of 2026-11-11: a create is never retried on a 502, 503 or 504
// unless the server says Should-Retry: true. Here a proxy in front of the
// gateway answers 502 with no header after the gateway has already started
// the sandbox; a retry would have started a second, billed one.
func TestCreateIsNotRetriedOnProxy502(t *testing.T) {
	e := newEnv(t)
	e.srv.InjectFault(fakee2b.Fault{Method: "POST", Path: "/v2/sandboxes", Status: 502, Times: 1, AfterHandling: true})

	r := e.run("create", "-t", "base", "--json")
	if r.code != 1 {
		t.Fatalf("create: exit %d, want 1\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	if got := countRequests(e, "POST", "/v2/sandboxes"); got != 1 {
		t.Errorf("POST /v2/sandboxes sent %d times, want exactly 1", got)
	}
	errObj := jsonLines(t, r.stderr)[0]["error"].(map[string]any)
	if errObj["status"] != float64(502) || errObj["outcome_unknown"] != true ||
		!strings.Contains(errObj["message"].(string), "list your sandboxes") {
		t.Errorf("error envelope: %v", errObj)
	}
	// The one sandbox the gateway did start is all there is.
	if got := len(jsonList(t, e.must("list", "--json").stdout)); got != 1 {
		t.Errorf("list: %d sandboxes, want the 1 the first create started", got)
	}

	// The same for fork, with a 504.
	id := jsonList(t, e.must("list", "--json").stdout)[0]["sandbox_id"].(string)
	e.srv.InjectFault(fakee2b.Fault{Method: "POST", Path: "/sandboxes/" + id + "/fork", Status: 504, Times: 1})
	if r := e.run("fork", id); r.code != 1 || !strings.Contains(r.stderr, "not retried") {
		t.Errorf("fork: exit %d, stderr %q", r.code, r.stderr)
	}
	if got := countRequests(e, "POST", "/sandboxes/"+id+"/fork"); got != 1 {
		t.Errorf("POST fork sent %d times, want exactly 1", got)
	}
}

// The gateway's 503 sandbox_capacity_unavailable says Should-Retry: true (and
// Retry-After), so a create still retries it.
func TestCreateRetries503WithShouldRetryTrue(t *testing.T) {
	e := newEnv(t)
	e.srv.InjectFault(fakee2b.Fault{Method: "POST", Path: "/v2/sandboxes", Status: 503, Times: 1,
		Header: map[string]string{"Should-Retry": "true", "Retry-After": "0"}})

	r := e.must("create", "-t", "base", "--json")
	if got := len(jsonLines(t, r.stdout)); got != 1 {
		t.Fatalf("create: %q", r.stdout)
	}
	if got := countRequests(e, "POST", "/v2/sandboxes"); got != 2 {
		t.Errorf("POST /v2/sandboxes sent %d times, want 2 (one retry)", got)
	}
}

// A GET is safe to repeat, so a 502 with no Should-Retry is retried as before.
func TestGetRetriesProxy502(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	e.srv.InjectFault(fakee2b.Fault{Method: "GET", Path: "/sandboxes/" + id, Status: 502, Times: 1})

	e.must("info", id, "--json")
	if got := countRequests(e, "GET", "/sandboxes/"+id); got != 2 {
		t.Errorf("GET /sandboxes/%s sent %d times, want 2 (one retry)", id, got)
	}
}
