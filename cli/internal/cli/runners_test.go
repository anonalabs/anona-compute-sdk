package cli_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// runnersFake serves the gateway's /v1/runners routes (task 13's contract)
// and a billing document whose per-sandbox vCPU cap stands for the plan.
type runnersFake struct {
	mu       sync.Mutex
	queries  []string
	sizes    []string
	eligible bool
}

func newRunnersEnv(t *testing.T, sizes ...string) (*env, *runnersFake) {
	t.Helper()
	f := &runnersFake{sizes: sizes, eligible: len(sizes) > 0}
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-API-Key") != testKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	js := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("GET /v1/runners/installations", auth(func(w http.ResponseWriter, r *http.Request) {
		reason := ""
		if !f.eligible {
			reason = "runners need a paid plan"
		}
		js(w, 200, map[string]any{"eligible": f.eligible, "reason": reason, "allowedSizes": f.sizes,
			"installations": []map[string]any{{"id": 12345, "accountLogin": "acme-org", "accountType": "Organization",
				"suspended": false, "linkedAt": "2026-11-21T10:00:00Z"}}})
	}))
	mux.HandleFunc("POST /v1/runners/installations/link", auth(func(w http.ResponseWriter, r *http.Request) {
		if !f.eligible {
			js(w, 403, map[string]any{"code": 403, "error_code": "runners_not_eligible", "message": "runners need a paid plan"})
			return
		}
		js(w, 200, map[string]any{"url": "https://github.com/apps/anona/installations/new?state=abc"})
	}))
	mux.HandleFunc("DELETE /v1/runners/installations/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.queries = append(f.queries, "DELETE "+r.PathValue("id"))
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	job := map[string]any{"id": "job-1", "repo": "acme/api", "workflow": "ci", "jobName": "test", "label": "anona-medium",
		"size": "medium", "status": "completed", "reason": "", "sandboxId": "sbx-9", "queuedAt": "2026-11-21T10:00:00Z",
		"startedAt": "2026-11-21T10:00:05Z", "finishedAt": "2026-11-21T10:02:35Z", "conclusion": "success",
		"githubUrl": "https://github.com/acme/api/actions/runs/1/job/2"}
	mux.HandleFunc("GET /v1/runners/jobs", auth(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.queries = append(f.queries, r.URL.RawQuery)
		f.mu.Unlock()
		js(w, 200, map[string]any{"jobs": []map[string]any{job}})
	}))
	mux.HandleFunc("GET /v1/runners/jobs/{id}", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "job-1" {
			js(w, 404, map[string]any{"message": "not found"})
			return
		}
		js(w, 200, job)
	}))
	hs := httptest.NewServer(mux)
	t.Cleanup(hs.Close)
	e := newEnv(t)
	e.url = hs.URL
	return e, f
}

func TestRunnersJobsTable(t *testing.T) {
	e, f := newRunnersEnv(t, "small", "medium")
	r := e.must("runners", "jobs", "--repo", "acme/api", "--status", "completed")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != "ID REPO JOB SIZE STATUS STARTED DURATION" {
		t.Fatalf("header: %q", lines[0])
	}
	if !strings.Contains(lines[1], "job-1") || !strings.Contains(lines[1], "acme/api") || !strings.Contains(lines[1], "2m30s") {
		t.Fatalf("row: %q", lines[1])
	}
	q := f.queries[0]
	for _, want := range []string{"repo=acme%2Fapi", "status=completed", "limit=20"} {
		if !strings.Contains(q, want) {
			t.Errorf("query %q misses %s", q, want)
		}
	}
}

func TestRunnersJobsJSONSnakeCase(t *testing.T) {
	e, _ := newRunnersEnv(t, "small", "medium")
	r := e.must("runners", "jobs", "--json")
	var out []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil || len(out) != 1 {
		t.Fatalf("%v: %s", err, r.stdout)
	}
	for _, k := range []string{"job_name", "sandbox_id", "queued_at", "github_url"} {
		if _, ok := out[0][k]; !ok {
			t.Errorf("missing %s in %v", k, out[0])
		}
	}
}

func TestRunnersJobDetail(t *testing.T) {
	e, _ := newRunnersEnv(t, "small", "medium")
	r := e.must("runners", "job", "job-1")
	for _, want := range []string{"job-1", "acme/api", "sbx-9", "success", "https://github.com/acme/api/actions/runs/1/job/2"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("job output misses %q:\n%s", want, r.stdout)
		}
	}
	if r := e.run("runners", "job", "nope"); r.code == 0 {
		t.Error("unknown job exited 0")
	}
}

func TestRunnersStatusSnippetOnlyAllowedSizes(t *testing.T) {
	e, _ := newRunnersEnv(t, "small", "medium") // Starter
	r := e.must("runners", "status")
	if !strings.Contains(r.stdout, "acme-org") {
		t.Errorf("installation not shown:\n%s", r.stdout)
	}
	for _, want := range []string{"runs-on: anona-small", "runs-on: anona-medium"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("misses %q:\n%s", want, r.stdout)
		}
	}
	for _, bad := range []string{"anona-large", "anona-xl"} {
		if strings.Contains(r.stdout, bad) {
			t.Errorf("shows %q above the plan:\n%s", bad, r.stdout)
		}
	}
}

func TestRunnersConnectNoBrowserPrintsURL(t *testing.T) {
	e, _ := newRunnersEnv(t, "small", "medium")
	r := e.must("runners", "connect", "--no-browser")
	if !strings.Contains(r.stdout, "https://github.com/apps/anona/installations/new?state=abc") {
		t.Fatalf("url not printed:\n%s", r.stdout)
	}
}

func TestRunnersDisconnect(t *testing.T) {
	e, f := newRunnersEnv(t, "small", "medium")
	e.must("runners", "disconnect", "12345")
	if len(f.queries) != 1 || f.queries[0] != "DELETE 12345" {
		t.Fatalf("requests: %v", f.queries)
	}
}

func TestRunnersStatusIneligibleNoSnippets(t *testing.T) {
	e, _ := newRunnersEnv(t)
	r := e.must("runners", "status")
	if !strings.Contains(r.stdout, "runners need a paid plan") || strings.Contains(r.stdout, "runs-on") {
		t.Fatalf("ineligible status:\n%s", r.stdout)
	}
}

func TestRunnersConnectNotEligible(t *testing.T) {
	e, _ := newRunnersEnv(t)
	r := e.run("runners", "connect", "--no-browser")
	if r.code == 0 || !strings.Contains(r.stderr, "runners need a paid plan") {
		t.Fatalf("exit %d stderr %q", r.code, r.stderr)
	}
}
