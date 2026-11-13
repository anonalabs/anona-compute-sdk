package cli_test

import (
	"strings"
	"testing"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/fakee2b"
)

func TestDoctor(t *testing.T) {
	e := newEnv(t)
	r := e.run("doctor")
	if r.code != 0 {
		t.Fatalf("doctor: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	for _, want := range []string{
		"ok    config     profile default", "API key from ANONA_COMPUTE_API_KEY e2b_", "4567",
		"ok    api        " + e.url + " is up, gateway fake-1, CLI " + testVersion,
		"ok    auth", "ok    templates  7 built-in: base, python", "ssh",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("doctor output lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, testKey) {
		t.Fatalf("doctor printed the key:\n%s", r.stdout)
	}
	checks := jsonLines(t, e.must("doctor", "--json").stdout)
	names := []string{}
	for _, c := range checks {
		names = append(names, c["check"].(string)+"="+c["status"].(string))
	}
	if got := strings.Join(names, " "); !strings.HasPrefix(got, "config=ok api=ok auth=ok templates=ok ssh=") {
		t.Fatalf("checks %s", got)
	}

	// Engine asleep: still ok, with the reason.
	e.srv.Configure(func(s *fakee2b.Server) { s.CatalogEngine = "paused" })
	if r := e.must("doctor"); !strings.Contains(r.stdout, "not yet confirmed") {
		t.Fatalf("asleep: %s", r.stdout)
	}

	// A refused key fails auth and skips what depends on it: exit 1.
	e.extra["ANONA_COMPUTE_API_KEY"] = "e2b_ffff"
	r = e.run("doctor")
	if r.code != 1 || !strings.Contains(r.stdout, "fail  auth") || !strings.Contains(r.stdout, "skip  templates") {
		t.Fatalf("bad key: exit %d\n%s", r.code, r.stdout)
	}

	// No credential at all: config fails.
	e.extra["ANONA_COMPUTE_API_KEY"] = ""
	e.extra["E2B_API_KEY"] = ""
	r = e.run("doctor")
	if r.code != 1 || !strings.Contains(r.stdout, "fail  config") || !strings.Contains(r.stdout, "anona-compute login") {
		t.Fatalf("no credential: exit %d\n%s", r.code, r.stdout)
	}

	// Gateway unreachable: api fails.
	e.extra["ANONA_COMPUTE_API_KEY"] = testKey
	e.extra["ANONA_COMPUTE_API_URL"] = "http://127.0.0.1:1"
	r = e.run("doctor", "--request-timeout", "2s")
	if r.code != 1 || !strings.Contains(r.stdout, "fail  api") || !strings.Contains(r.stdout, "skip  auth") {
		t.Fatalf("unreachable: exit %d\n%s", r.code, r.stdout)
	}
}
