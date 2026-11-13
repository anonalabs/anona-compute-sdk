package cli_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
)

func TestRootVersionFlag(t *testing.T) {
	e := newEnv(t)
	if r := e.must("--version"); r.stdout != testVersion+"\n" {
		t.Fatalf("--version: %q", r.stdout)
	}
	if r := e.must("--version"); r.stdout != e.must("version").stdout {
		t.Fatalf("--version and version differ")
	}
	got := jsonLines(t, e.must("--version", "--json").stdout)
	if len(got) != 1 || got[0]["version"] != testVersion {
		t.Fatalf("--version --json: %v", got)
	}
	got = jsonLines(t, e.must("-o", "json", "--version").stdout)
	if len(got) != 1 || got[0]["version"] != testVersion {
		t.Fatalf("-o json --version: %v", got)
	}
}

var snakeRE = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*$`)

// checkSnake fails on any key that is not snake_case, except inside
// metadata, envs and log fields, whose keys belong to the user.
func checkSnake(t *testing.T, where string, v any) {
	t.Helper()
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if !snakeRE.MatchString(k) {
				t.Errorf("%s: key %q is not snake_case", where, k)
			}
			if k == "metadata" || k == "fields" || k == "envs" || k == "env_vars" {
				continue
			}
			checkSnake(t, where+"."+k, val)
		}
	case []any:
		for _, val := range x {
			checkSnake(t, where, val)
		}
	}
}

// Every --json output uses snake_case keys (as the MCP server does).
func TestJSONKeysAreSnakeCase(t *testing.T) {
	e := newEnv(t)
	e.extra["ANONA_COMPUTE_TEST_BG_DIR"] = t.TempDir()
	created := jsonLines(t, e.must("create", "--json", "--metadata", "myKey=1").stdout)
	id := created[0]["sandbox_id"].(string)
	cmds := [][]string{
		{"version"}, {"list"}, {"list", "--state", "all"}, {"info", id}, {"timeout", id, "10m"},
		{"exec", id, "--", "echo hi; echo err >&2"}, {"run", "--", "true"}, {"whoami"},
		{"config", "list"}, {"usage"}, {"usage", "--billing"}, {"ps", id}, {"preview", id, "3000"},
		{"pause", id}, {"resume", id}, {"fork", id}, {"delete", id},
	}
	for _, c := range cmds {
		r := e.run(append([]string{"--json"}, c...)...)
		if r.code != 0 {
			t.Errorf("%v: exit %d %s", c, r.code, r.stderr)
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(r.stdout), "\n") {
			var doc any
			if err := json.Unmarshal([]byte(line), &doc); err != nil {
				t.Errorf("%v: not JSON: %q", c, line)
				continue
			}
			checkSnake(t, strings.Join(c, " "), doc)
		}
	}
	md := jsonList(t, e.must("--json", "list").stdout)
	for _, l := range md {
		if m, ok := l["metadata"].(map[string]any); ok && m["myKey"] != "1" {
			t.Errorf("metadata keys must be printed as given: %v", m)
		}
	}
}

// Errors: one envelope shape; the gateway's request ID is kept and shown.
func TestErrorEnvelope(t *testing.T) {
	e := newEnv(t)
	r := e.run("info", "nope")
	if r.code != 4 || !strings.Contains(r.stderr, "(request req-fake-404)") {
		t.Fatalf("text error: exit %d %q", r.code, r.stderr)
	}
	r = e.run("--json", "info", "nope")
	l := jsonLines(t, r.stderr)
	obj := l[0]["error"].(map[string]any)
	if obj["exit_code"] != float64(4) || obj["status"] != float64(404) || obj["request_id"] != "req-fake-404" || obj["message"] == "" {
		t.Fatalf("json error: %v", obj)
	}
	checkSnake(t, "error", l[0])

	// No HTTP answer at all: still the same envelope, with a network code.
	e.extra["ANONA_COMPUTE_API_URL"] = "http://127.0.0.1:1"
	r = e.run("--json", "list")
	l = jsonLines(t, r.stderr)
	obj = l[0]["error"].(map[string]any)
	if r.code != 1 || obj["exit_code"] != float64(1) || obj["error_code"] != "network_error" || obj["status"] != nil {
		t.Fatalf("network error: exit %d %v", r.code, obj)
	}
}
