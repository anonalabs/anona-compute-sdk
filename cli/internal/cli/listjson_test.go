package cli_test

import (
	"strings"
	"testing"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/fakee2b"
)

// Every list command's --json output is one JSON array, so an empty list is
// `[]` and not zero bytes. Zero bytes is not JSON: `list --json | jq` and
// JSON.parse both fail on it, which is how this was found.
func TestListCommandsPrintAnEmptyArray(t *testing.T) {
	e := newEnv(t)
	e.extra["ANONA_COMPUTE_TEST_BG_DIR"] = t.TempDir()
	e.srv.Configure(func(s *fakee2b.Server) { s.NoCatalog = true }) // nothing built-in either
	cases := [][]string{
		{"list"}, {"ls"}, {"list", "--state", "all"}, {"list", "--state", "paused"},
		{"template", "list"}, {"template", "list", "--all"}, {"template", "ls"},
		{"config", "list"}, // no config file: no profiles
	}
	for _, c := range cases {
		for _, flag := range [][]string{{"--json"}, {"-o", "json"}} {
			args := append(append([]string{}, c...), flag...)
			r := e.must(args...)
			if r.stdout != "[]\n" {
				t.Errorf("%s: stdout %q, want \"[]\\n\"", strings.Join(args, " "), r.stdout)
			}
			if r.stderr != "" {
				t.Errorf("%s: stderr %q, want nothing (JSON consumers get clean streams)", strings.Join(args, " "), r.stderr)
			}
		}
	}

	// ps needs a sandbox; one with no processes lists none.
	id := jsonLines(t, e.must("create", "--json").stdout)[0]["sandbox_id"].(string)
	if r := e.must("ps", id, "--json"); r.stdout != "[]\n" {
		t.Errorf("ps --json with no processes: %q", r.stdout)
	}
	// And a non-empty list is the same array, not one object per line.
	if got := jsonList(t, e.must("list", "--json").stdout); len(got) != 1 || got[0]["sandbox_id"] != id {
		t.Errorf("list --json: %v", got)
	}
}
