package cli_test

import (
	"net/url"
	"strings"
	"testing"
)

// A sandbox name is the "name" metadata key: create --name sets it, list and
// info show it, list --name filters on it and rename changes it. The ID stays
// what every command takes.
func TestSandboxNames(t *testing.T) {
	e := newEnv(t)
	id := strings.TrimSpace(e.must("create", "--name", "  eval runner 7  ", "--metadata", "team=x").stdout)
	body := lastRequest(t, e, "POST", "/v2/sandboxes")
	md, _ := body["metadata"].(map[string]any)
	if md["name"] != "eval runner 7" || md["team"] != "x" {
		t.Fatalf("create sent metadata %v", body["metadata"])
	}
	other := strings.TrimSpace(e.must("create").stdout)

	r := e.must("list")
	lines := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if !strings.HasPrefix(lines[0], "SANDBOX ID") || !strings.Contains(lines[0], "NAME") {
		t.Fatalf("list header: %q", lines[0])
	}
	var named, unnamed string
	for _, l := range lines[1:] {
		switch {
		case strings.HasPrefix(l, id):
			named = l
		case strings.HasPrefix(l, other):
			unnamed = l
		}
	}
	if !strings.Contains(named, "eval runner 7") || !strings.Contains(unnamed, " - ") {
		t.Fatalf("list rows:\n%s", r.stdout)
	}

	got := jsonList(t, e.must("list", "--name", "eval runner 7", "--json").stdout)
	if len(got) != 1 || got[0]["sandbox_id"] != id || got[0]["name"] != "eval runner 7" {
		t.Fatalf("list --name: %v", got)
	}
	if q := lastRequest(t, e, "GET", "/v2/sandboxes")["_query"].(url.Values).Get("metadata"); q != "name=eval+runner+7" {
		t.Fatalf("list --name query: %q", q)
	}
	if r := e.must("info", id); !strings.Contains(r.stdout, "Name") || !strings.Contains(r.stdout, "eval runner 7") {
		t.Fatalf("info: %q", r.stdout)
	}
	if r := e.must("info", other); strings.Contains(r.stdout, "Name") {
		t.Fatalf("info of an unnamed sandbox prints a Name line: %q", r.stdout)
	}

	r = e.must("rename", id, "nightly")
	if !strings.Contains(r.stderr, `now named "nightly"`) {
		t.Fatalf("rename: %q", r.stderr)
	}
	if b := lastRequest(t, e, "PUT", "/v1/sandboxes/"+id+"/name"); b["name"] != "nightly" {
		t.Fatalf("rename sent %v", b)
	}
	if sb, _ := e.srv.Sandbox(id); sb.Metadata["name"] != "nightly" || sb.Metadata["team"] != "x" {
		t.Fatalf("after rename: %v", sb.Metadata)
	}
	got = jsonLines(t, e.must("rename", id, "--clear", "--json").stdout)
	if len(got) != 1 || got[0]["sandbox_id"] != id || got[0]["name"] != "" {
		t.Fatalf("rename --clear: %v", got)
	}
	if sb, _ := e.srv.Sandbox(id); sb.Metadata["name"] != "" {
		t.Fatalf("after clear: %v", sb.Metadata)
	}
}

// A name the gateway would refuse is refused first, with no request sent.
func TestSandboxNameValidation(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	before := len(e.srv.Requests())
	for _, args := range [][]string{
		{"create", "--name", "a/b"},
		{"create", "--name", strings.Repeat("x", 65)},
		{"create", "--name", "-dash-first"},
		{"create", "--name", "one", "--metadata", "name=two"},
		{"rename", id},
		{"rename", id, "bad;name"},
		{"rename", id, "   "},
		{"rename", id, "x", "--clear"},
	} {
		if r := e.run(args...); r.code != 2 {
			t.Errorf("%v: exit %d %q, want a usage error", args, r.code, r.stderr)
		}
	}
	if n := len(e.srv.Requests()); n != before {
		t.Fatalf("validation made %d requests", n-before)
	}
	// The same name in both places is not a disagreement.
	e.must("create", "--name", "same", "--metadata", "name=same")
}
