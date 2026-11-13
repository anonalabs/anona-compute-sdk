package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Golden help for the root and every subcommand.
func TestHelpGolden(t *testing.T) {
	e := newEnv(t)
	cmds := [][]string{
		{}, {"version"}, {"config"}, {"config", "get"}, {"config", "set"}, {"config", "list"},
		{"login"}, {"logout"}, {"create"}, {"list"}, {"info"}, {"delete"}, {"kill"}, {"pause"}, {"resume"},
		{"timeout"}, {"rename"}, {"fork"}, {"exec"}, {"run"}, {"logs"},
		{"whoami"}, {"keys"}, {"connect"}, {"ssh"}, {"scp"}, {"view"}, {"desktop"}, {"template"}, {"template", "build"},
		{"limits"}, {"push"}, {"recover"}, {"mcp"}, {"sandbox"}, {"doctor"}, {"output"}, {"ps"}, {"usage"}, {"preview"},
		{"vault"}, {"vault", "list"}, {"vault", "create"}, {"vault", "rotate"},
		{"vault", "allowed-hosts"}, {"vault", "delete"}, {"vault", "unbind"},
	}
	for _, c := range cmds {
		name := "root"
		if len(c) > 0 {
			name = strings.Join(c, "_")
		}
		t.Run(name, func(t *testing.T) {
			r := e.must(append(c, "--help")...)
			golden := filepath.Join("testdata", "help", name+".golden")
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, []byte(r.stdout), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run go test -run TestHelpGolden -update)", err)
			}
			if r.stdout != string(want) {
				t.Errorf("help for %q changed:\n--- got\n%s\n--- want\n%s", name, r.stdout, want)
			}
		})
	}
}

func TestUsageErrorsExit2(t *testing.T) {
	e := newEnv(t)
	for _, args := range [][]string{
		{"--no-such-flag"},
		{"list", "--no-such-flag"},
		{"no-such-command"},
		{"info"},
		{"timeout", "x"},
		{"list", "--state", "weird"},
		{"-o", "yaml", "version"},
		{"kill", "--all"},
		{"fork", "x", "--count", "21"},
	} {
		r := e.run(args...)
		if r.code != 2 {
			t.Errorf("anona %v: exit %d, want 2 (stderr %q)", args, r.code, r.stderr)
		}
	}
}

func TestVersionJSON(t *testing.T) {
	e := newEnv(t)
	r := e.must("version", "-o", "json")
	lines := jsonLines(t, r.stdout)
	if len(lines) != 1 || lines[0]["version"] != testVersion {
		t.Fatalf("got %q", r.stdout)
	}
	r = e.must("version", "--json")
	if got := jsonLines(t, r.stdout); got[0]["version"] != testVersion {
		t.Fatalf("got %q", r.stdout)
	}
	if r := e.must("version"); strings.TrimSpace(r.stdout) != testVersion {
		t.Fatalf("got %q", r.stdout)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	e := newEnv(t)
	e.extra["ANONA_COMPUTE_API_KEY"] = ""
	e.must("config", "set", "api_url", e.url)
	e.must("config", "set", "domain", "compute.example.test")
	e.must("--profile", "work", "config", "set", "default_template", "desktop")
	r := e.runIn(testKey+"\n", "login", "--with-api-key")
	if r.code != 0 || !strings.Contains(r.stderr, "Saved and verified API key") {
		t.Fatalf("login: %d %s", r.code, r.stderr)
	}
	path := filepath.Join(e.cfgDir, "anona-compute", "config.toml")
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v, want 0600", st.Mode().Perm())
	}
	data, _ := os.ReadFile(path)
	t.Logf("config.toml:\n%s", data)
	if got := strings.TrimSpace(e.must("config", "get", "domain").stdout); got != "compute.example.test" {
		t.Fatalf("domain = %q", got)
	}
	if got := strings.TrimSpace(e.must("--profile", "work", "config", "get", "default_template").stdout); got != "desktop" {
		t.Fatalf("default_template = %q", got)
	}
	// config get masks the key like config list, unless --reveal.
	if got := strings.TrimSpace(e.must("config", "get", "api_key").stdout); got != "e2b_"+strings.Repeat("*", 36)+"4567" {
		t.Fatalf("api_key = %q", got)
	}
	if got := jsonLines(t, e.must("config", "get", "api_key", "--json").stdout); got[0]["value"] == testKey {
		t.Fatalf("config get --json leaks the key: %v", got)
	}
	if got := strings.TrimSpace(e.must("config", "get", "api_key", "--reveal").stdout); got != testKey {
		t.Fatalf("api_key --reveal = %q", got)
	}
	list := e.must("config", "list", "--json")
	lines := jsonLines(t, list.stdout)
	if len(lines) != 2 {
		t.Fatalf("config list: %q", list.stdout)
	}
	if k := lines[0]["api_key"].(string); strings.Contains(k, "0123456789abcdef0123") {
		t.Fatalf("api key not masked: %q", k)
	}
	// The saved key is used: list works without ANONA_COMPUTE_API_KEY.
	e.must("list")
	// Wrong key format is rejected before saving: gateway keys are e2b_ +
	// 40 lowercase hex.
	for _, bad := range []string{"sk-nope", "e2b_abc", "e2b_" + strings.Repeat("A", 40), testKey + "0"} {
		if r := e.runIn(bad+"\n", "login", "--with-api-key"); r.code != 2 {
			t.Fatalf("bad key %q: exit %d", bad, r.code)
		}
		if r := e.run("config", "set", "api_key", bad); r.code != 2 {
			t.Fatalf("config set bad key %q: exit %d", bad, r.code)
		}
	}
	if r := e.run("config", "set", "nope", "x"); r.code != 2 {
		t.Fatalf("unknown key: exit %d", r.code)
	}
	e.must("logout")
	if r := e.run("list"); r.code != 3 {
		t.Fatalf("after logout list exit %d, want 3: %s", r.code, r.stderr)
	}
}

// login --with-api-key checks the saved key against the gateway.
func TestLoginWithKeyVerifies(t *testing.T) {
	e := newEnv(t)
	e.extra["ANONA_COMPUTE_API_KEY"] = ""
	wrong := "e2b_" + strings.Repeat("ab", 20)
	r := e.runIn(wrong+"\n", "login", "--with-api-key")
	if r.code != 3 || !strings.Contains(r.stderr, "Saved API key") || !strings.Contains(r.stderr, "the gateway rejected it (401)") {
		t.Fatalf("rejected key: exit %d %q", r.code, r.stderr)
	}
	// It was saved all the same.
	if got := strings.TrimSpace(e.must("config", "get", "api_key", "--reveal").stdout); got != wrong {
		t.Fatalf("saved %q", got)
	}
	r = e.runIn(wrong+"\n", "login", "--with-api-key", "--no-verify")
	if r.code != 0 || !strings.Contains(r.stderr, "not verified") {
		t.Fatalf("--no-verify: exit %d %q", r.code, r.stderr)
	}
	// The team name comes from usage when the key may read it.
	r = e.runIn(testKey+"\n", "login", "--with-api-key", "--json")
	got := jsonLines(t, r.stdout)
	if r.code != 0 || len(got) != 1 || got[0]["verified"] != true || got[0]["team"] == nil {
		t.Fatalf("verified json: exit %d %v %s", r.code, got, r.stderr)
	}
	// Gateway unreachable: saved, not verified, exit 0.
	e.extra["ANONA_COMPUTE_API_URL"] = "http://127.0.0.1:1"
	r = e.runIn(testKey+"\n", "login", "--with-api-key")
	if r.code != 0 || !strings.Contains(r.stderr, "could not verify it") {
		t.Fatalf("unreachable: exit %d %q", r.code, r.stderr)
	}
}

func TestNotAvailableYet(t *testing.T) {
	e := newEnv(t)
	for _, c := range []string{"keys", "limits", "push", "recover"} {
		r := e.run(c, "some-id")
		if r.code != 1 || !strings.Contains(r.stderr, "not available yet") {
			t.Errorf("%s: exit %d stderr %q", c, r.code, r.stderr)
		}
	}
	r := e.run("keys", "x", "--json")
	lines := jsonLines(t, r.stderr)
	if len(lines) != 1 || lines[0]["error"] == nil {
		t.Errorf("keys --json stderr %q", r.stderr)
	}
}
