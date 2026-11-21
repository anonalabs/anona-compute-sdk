package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/mcpserver"
)

type runnersFake struct {
	mu       sync.Mutex
	eligible bool
	paths    []string
	queries  []url.Values
}

func (f *runnersFake) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.paths)
}

func connectRunners(t *testing.T, eligible bool) (*mcp.ClientSession, *runnersFake) {
	t.Helper()
	f := &runnersFake{eligible: eligible}
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.paths = append(f.paths, r.URL.Path)
		f.queries = append(f.queries, r.URL.Query())
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/runners/installations":
			if f.eligible {
				w.Write([]byte(`{"eligible":true,"reason":"","allowedSizes":["small","medium"],"installations":[{"id":7,"accountLogin":"acme","accountType":"Organization","suspended":false,"linkedAt":"2026-11-20T10:00:00Z"}]}`))
			} else {
				w.Write([]byte(`{"eligible":false,"reason":"Runners need a paid plan","allowedSizes":[],"installations":[]}`))
			}
		case "/v1/runners/jobs":
			w.Write([]byte(`{"jobs":[{"id":"j1","repo":"acme/app","workflow":"ci","jobName":"test","label":"anona-small","size":"small","status":"running","sandboxId":"sbx1","githubUrl":"https://github.com/acme/app/actions/runs/1"}]}`))
		case "/v1/runners/jobs/j1":
			w.Write([]byte(`{"id":"j1","repo":"acme/app","status":"completed","conclusion":"success"}`))
		default:
			w.WriteHeader(404)
			w.Write([]byte(`{"code":404,"message":"job not found"}`))
		}
	}))
	t.Cleanup(hs.Close)
	srv := mcpserver.New(mcpserver.Config{
		API:      &api.Client{BaseURL: hs.URL, APIKey: key, HTTP: hs.Client()},
		ReadOnly: true,
		Runners:  true,
		Version:  "test",
	})
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, f
}

// rejected reports whether a call failed, as a protocol error (schema
// validation) or a tool error.
func rejected(cs *mcp.ClientSession, name string, args map[string]any) bool {
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	return err != nil || res.IsError
}

func TestRunnersStatus(t *testing.T) {
	cs, _ := connectRunners(t, true)
	out, res := call(t, cs, "runners_status", map[string]any{})
	if res.IsError || out["eligible"] != true {
		t.Fatalf("status: %v %v", out, res.Content)
	}
	b, _ := json.Marshal(out["runsOn"])
	var runsOn []string
	_ = json.Unmarshal(b, &runsOn)
	if !slices.Equal(runsOn, []string{"runs-on: anona-small", "runs-on: anona-medium"}) {
		t.Fatalf("runsOn %v", runsOn)
	}
	inst, _ := out["installations"].([]any)
	if len(inst) != 1 || inst[0].(map[string]any)["accountLogin"] != "acme" {
		t.Fatalf("installations %v", out["installations"])
	}
}

func TestRunnersStatusIneligible(t *testing.T) {
	cs, _ := connectRunners(t, false)
	out, res := call(t, cs, "runners_status", map[string]any{})
	if res.IsError || out["eligible"] != false || out["reason"] != "Runners need a paid plan" {
		t.Fatalf("status: %v", out)
	}
	if r, _ := out["runsOn"].([]any); len(r) != 0 {
		t.Fatalf("runsOn for ineligible: %v", r)
	}
}

func TestRunnersJobsQuery(t *testing.T) {
	cs, f := connectRunners(t, true)
	out, res := call(t, cs, "runners_jobs", map[string]any{"repo": "acme/app", "status": "running", "limit": 5})
	if res.IsError {
		t.Fatalf("jobs: %v", res.Content)
	}
	jobs, _ := out["jobs"].([]any)
	if len(jobs) != 1 || jobs[0].(map[string]any)["sandboxId"] != "sbx1" {
		t.Fatalf("jobs %v", out)
	}
	q := f.queries[len(f.queries)-1]
	if q.Get("repo") != "acme/app" || q.Get("status") != "running" || q.Get("limit") != "5" {
		t.Fatalf("query %v", q)
	}
	call(t, cs, "runners_jobs", map[string]any{})
	if q := f.queries[len(f.queries)-1]; q.Get("limit") != "20" {
		t.Fatalf("default limit %v", q)
	}
}

func TestRunnersJobsValidation(t *testing.T) {
	cs, f := connectRunners(t, true)
	for _, args := range []map[string]any{{"limit": 0}, {"limit": 501}, {"status": "bogus"}} {
		if !rejected(cs, "runners_jobs", args) {
			t.Errorf("accepted %v", args)
		}
	}
	if f.count() != 0 {
		t.Fatalf("API called %d times for invalid input", f.count())
	}
}

func TestRunnersJob(t *testing.T) {
	cs, _ := connectRunners(t, true)
	out, res := call(t, cs, "runners_job", map[string]any{"id": "j1"})
	if res.IsError || out["conclusion"] != "success" {
		t.Fatalf("job: %v %v", out, res.Content)
	}
	if !rejected(cs, "runners_job", map[string]any{"id": "nope"}) {
		t.Fatal("404 not surfaced as an error")
	}
}
