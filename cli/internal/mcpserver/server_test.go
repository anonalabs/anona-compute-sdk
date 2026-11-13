package mcpserver_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/envd"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/fakee2b"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/mcpserver"
)

const key = "e2b_0123456789abcdef0123456789abcdef"

// connect runs the server against the fake gateway (fakee2b, which also
// serves envd's process service) over in-memory MCP transports.
func connect(t *testing.T, readOnly bool) (*mcp.ClientSession, *fakee2b.Server) {
	t.Helper()
	fake := fakee2b.New(key)
	hs := httptest.NewServer(fake.Handler())
	t.Cleanup(hs.Close)
	srv := mcpserver.New(mcpserver.Config{
		API:      &api.Client{BaseURL: hs.URL, APIKey: key, HTTP: hs.Client()},
		ReadOnly: readOnly,
		Version:  "test",
		Envd: func(id, token, version string, domain *string, user string) *envd.Client {
			return &envd.Client{BaseURL: hs.URL, SandboxID: id, AccessToken: token, EnvdVersion: version, User: user, HTTP: &http.Client{}}
		},
		Whoami: func(context.Context) (map[string]any, error) { return map[string]any{"auth": "api_key"}, nil },
	})
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, fake
}

func toolNames(t *testing.T, cs *mcp.ClientSession) []string {
	t.Helper()
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
		if tl.Description == "" || tl.InputSchema == nil || tl.Annotations == nil {
			t.Errorf("%s: missing description, schema or annotations", tl.Name)
		}
	}
	return names
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (map[string]any, *mcp.CallToolResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out map[string]any
	b, _ := json.Marshal(res.StructuredContent)
	_ = json.Unmarshal(b, &out)
	return out, res
}

func TestReadOnlyToolSet(t *testing.T) {
	cs, _ := connect(t, true)
	names := toolNames(t, cs)
	want := []string{"get_live_view_url", "get_sandbox", "get_sandbox_logs", "get_sandbox_metrics", "get_usage",
		"list_sandboxes", "list_templates", "whoami"}
	slices.Sort(names)
	if !slices.Equal(names, want) {
		t.Fatalf("read-only tools %v, want %v", names, want)
	}
	if !strings.Contains(cs.InitializeResult().Instructions, "read-only") {
		t.Fatalf("instructions do not say read-only: %q", cs.InitializeResult().Instructions)
	}
	// A tool that is not offered cannot be called either.
	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "kill_sandbox", Arguments: map[string]any{"sandbox_id": "x"}}); err == nil {
		t.Fatal("kill_sandbox callable in a read-only session")
	}
}

func TestCreateRunKill(t *testing.T) {
	cs, fake := connect(t, false)
	if n := len(toolNames(t, cs)); n != 22 { // get_ssh_config is off without an SSH func
		t.Fatalf("%d tools", n)
	}
	out, res := call(t, cs, "create_sandbox", map[string]any{"template": "base", "timeout_seconds": 120, "internet": false, "metadata": map[string]any{"k": "v"}})
	if res.IsError {
		t.Fatalf("create: %v", res.Content)
	}
	id := out["sandbox_id"].(string)
	var body map[string]any
	for _, r := range fake.Requests() {
		if r.Method == "POST" && r.Path == "/v2/sandboxes" {
			_ = json.Unmarshal([]byte(r.Body), &body)
		}
	}
	if body["templateID"] != "base" || body["timeout"] != float64(120) || body["allow_internet_access"] != false {
		t.Fatalf("create body %v", body)
	}

	out, res = call(t, cs, "run_command", map[string]any{"sandbox_id": id, "command": "echo hi; echo oops >&2; exit 7", "env": map[string]any{"A": "1"}})
	if res.IsError || out["exit_code"] != float64(7) || out["stdout"] != "hi\n" || out["stderr"] != "oops\n" {
		t.Fatalf("run_command: %v %v", out, res.Content)
	}
	st := fake.Starts()
	if len(st) != 1 || st[0].Cmd != "/bin/bash" || st[0].Args[1] != "-c" || st[0].Envs["A"] != "1" {
		t.Fatalf("envd start %+v", st)
	}

	// Input validation happens before the handler.
	if _, res := call(t, cs, "run_command", map[string]any{"sandbox_id": id}); !res.IsError {
		t.Fatal("run_command without command accepted")
	}
	if _, res := call(t, cs, "list_sandboxes", map[string]any{"state": "dead"}); !res.IsError {
		t.Fatal("bad state enum accepted")
	}

	if _, res := call(t, cs, "kill_sandbox", map[string]any{"sandbox_id": id}); res.IsError {
		t.Fatalf("kill: %v", res.Content)
	}
	// fakee2b's simulated gateway still 404s a second kill; the real gateway
	// no longer does (bug #8, lifecycle.go's kill is now idempotent). This
	// assertion is against the fake only and does not describe production
	// kill_sandbox behavior - bringing the fake in line is out of that
	// task's scope.
	_, res = call(t, cs, "kill_sandbox", map[string]any{"sandbox_id": id})
	if !res.IsError || !strings.Contains(res.Content[0].(*mcp.TextContent).Text, "404") {
		t.Fatalf("second kill: %v", res.Content)
	}
}

// TestKillProcessIdempotent: kill_process on a PID that is already gone (or
// never existed) is success, mirroring pause_sandbox's already_paused
// pattern, not a tool error (bug #8).
func TestKillProcessIdempotent(t *testing.T) {
	cs, _ := connect(t, false)
	out, res := call(t, cs, "create_sandbox", map[string]any{"template": "base", "timeout_seconds": 120, "internet": false})
	if res.IsError {
		t.Fatalf("create: %v", res.Content)
	}
	id := out["sandbox_id"].(string)

	out, res = call(t, cs, "kill_process", map[string]any{"sandbox_id": id, "pid": 999999})
	if res.IsError {
		t.Fatalf("kill_process on an unknown pid: %v", res.Content)
	}
	if out["status"] != "already_exited" {
		t.Fatalf("status = %v, want already_exited", out["status"])
	}
}

func TestListTemplates(t *testing.T) {
	cs, fake := connect(t, true)
	fake.Templates = []map[string]any{{"templateID": "abc123", "buildID": "b1", "cpuCount": 2, "memoryMB": 2048, "diskSizeMB": 1024,
		"public": false, "aliases": []string{"my-env"}, "names": []string{"my-env"}, "createdAt": "2026-10-28T10:00:00Z",
		"updatedAt": "2026-10-28T10:05:00Z", "createdBy": nil, "lastSpawnedAt": nil, "spawnCount": 0, "buildCount": 1,
		"envdVersion": "0.5.13", "buildStatus": "ready", "phase": "ready"}}
	out, res := call(t, cs, "list_templates", map[string]any{})
	if res.IsError {
		t.Fatalf("list_templates: %v", res.Content)
	}
	ts := out["templates"].([]any)
	if len(ts) != 1 {
		t.Fatalf("templates %v", out)
	}
	tpl := ts[0].(map[string]any)
	if tpl["template_id"] != "abc123" || tpl["status"] != "ready" || tpl["names"].([]any)[0] != "my-env" || tpl["updated_at"] != "2026-10-28T10:05:00Z" {
		t.Fatalf("template %v", tpl)
	}
}

// TestEndAtNullForNoDeadline: a sandbox with no time limit (extend_sandbox
// timeout_seconds -1) must report end_at as JSON null, not "" (bug #9). A
// sandbox with a real deadline must still report a valid timestamp string.
func TestEndAtNullForNoDeadline(t *testing.T) {
	cs, _ := connect(t, false)
	out, res := call(t, cs, "create_sandbox", map[string]any{"template": "base", "timeout_seconds": 120})
	if res.IsError {
		t.Fatalf("create: %v", res.Content)
	}
	id := out["sandbox_id"].(string)

	// Normal sandbox: end_at must still be a valid, non-empty timestamp string.
	_, res = call(t, cs, "get_sandbox", map[string]any{"sandbox_id": id})
	if res.IsError {
		t.Fatalf("get_sandbox: %v", res.Content)
	}
	raw := res.Content[0].(*mcp.TextContent).Text
	var normal map[string]any
	if err := json.Unmarshal([]byte(raw), &normal); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	endAt, ok := normal["end_at"].(string)
	if !ok || endAt == "" {
		t.Fatalf("normal sandbox end_at = %v (%T), want non-empty string", normal["end_at"], normal["end_at"])
	}

	// Remove the deadline.
	if _, res := call(t, cs, "extend_sandbox", map[string]any{"sandbox_id": id, "timeout_seconds": -1}); res.IsError {
		t.Fatalf("extend_sandbox -1: %v", res.Content)
	}

	_, res = call(t, cs, "get_sandbox", map[string]any{"sandbox_id": id})
	if res.IsError {
		t.Fatalf("get_sandbox: %v", res.Content)
	}
	raw = res.Content[0].(*mcp.TextContent).Text
	if strings.Contains(raw, `"end_at":""`) {
		t.Fatalf("get_sandbox raw JSON has end_at:\"\": %s", raw)
	}
	var noDeadline map[string]any
	if err := json.Unmarshal([]byte(raw), &noDeadline); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, present := noDeadline["end_at"]; present && v != nil {
		t.Fatalf("get_sandbox end_at = %v, want null or absent", v)
	}

	// list_sandboxes must agree.
	_, res = call(t, cs, "list_sandboxes", map[string]any{})
	if res.IsError {
		t.Fatalf("list_sandboxes: %v", res.Content)
	}
	raw = res.Content[0].(*mcp.TextContent).Text
	if strings.Contains(raw, `"end_at":""`) {
		t.Fatalf("list_sandboxes raw JSON has end_at:\"\": %s", raw)
	}
	var listOut map[string]any
	if err := json.Unmarshal([]byte(raw), &listOut); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	sbs := listOut["sandboxes"].([]any)
	found := false
	for _, s := range sbs {
		sb := s.(map[string]any)
		if sb["sandbox_id"] == id {
			found = true
			if v, present := sb["end_at"]; present && v != nil {
				t.Fatalf("list_sandboxes end_at = %v, want null or absent", v)
			}
		}
	}
	if !found {
		t.Fatalf("sandbox %s not found in list_sandboxes output: %s", id, raw)
	}
}

func TestGetUsage(t *testing.T) {
	cs, fake := connect(t, true) // read-only sessions (viewers) may read usage
	out, res := call(t, cs, "get_usage", map[string]any{})
	if res.IsError {
		t.Fatalf("get_usage: %v", res.Content)
	}
	totals, _ := out["totals"].(map[string]any)
	period, _ := out["period"].(map[string]any)
	if totals["amountUsd"] != 1.351 || period["key"] != "2026-10" || out["daily"] != nil {
		t.Fatalf("get_usage: %v", out)
	}
	out, res = call(t, cs, "get_usage", map[string]any{"period": "2026-09", "include_daily": true})
	if res.IsError || out["period"].(map[string]any)["key"] != "2026-09" || out["daily"] == nil {
		t.Fatalf("get_usage 2026-09: %v %v", out, res.Content)
	}
	last := fake.Requests()[len(fake.Requests())-1]
	if last.Path != "/v1/usage" || last.Query.Get("period") != "2026-09" || last.Header.Get("X-API-Key") != key {
		t.Fatalf("request %+v", last)
	}
	if _, res := call(t, cs, "get_usage", map[string]any{"period": "last week"}); !res.IsError {
		t.Fatal("bad period accepted")
	}
}
