package cli_test

import (
	"strings"
	"testing"
)

// A desktop or DOM view of a sandbox whose template has no desktop is
// refused before a link is minted; --force mints it anyway.
func TestDesktopNeedsDesktopTemplate(t *testing.T) {
	e := newEnv(t)
	base := e.srv.AddSandbox("base")
	desk := e.srv.AddSandbox("desktop")
	custom := e.srv.AddSandbox("my-custom")
	for _, args := range [][]string{
		{"desktop", base}, {"desktop", base, "--mode", "novnc"}, {"view", base, "--mode", "desktop"}, {"view", base, "--mode", "dom"},
	} {
		r := e.run(args...)
		if r.code != 2 || !strings.Contains(r.stderr, "base template, which has no desktop") || r.stdout != "" {
			t.Errorf("%v: exit %d stdout %q stderr %q", args, r.code, r.stdout, r.stderr)
		}
	}
	// desktop: allowed (novnc needs no gateway call after the check).
	if r := e.run("desktop", desk, "--mode", "novnc"); r.code != 0 || !strings.Contains(r.stdout, "6080-"+desk) {
		t.Fatalf("desktop template: exit %d %q %q", r.code, r.stdout, r.stderr)
	}
	// --force skips the check.
	if r := e.run("desktop", base, "--mode", "novnc", "--force"); r.code != 0 {
		t.Fatalf("--force: exit %d %q", r.code, r.stderr)
	}
	// A custom template might have a desktop: a warning, not a refusal.
	if r := e.run("desktop", custom, "--mode", "novnc"); r.code != 0 || !strings.Contains(r.stderr, "warning") {
		t.Fatalf("custom: exit %d %q", r.code, r.stderr)
	}
	// A terminal view needs no desktop.
	if r := e.run("view", base, "--mode", "terminal", "--control"); r.code == 2 {
		t.Fatalf("terminal view refused: %q", r.stderr)
	}
}

// Flag values the gateway would refuse are refused first, in flag terms.
func TestClientSideValidation(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("desktop")
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"view", id, "--ttl", "20s"}, "--ttl must be 30s to 10m"},
		{[]string{"view", id, "--ttl", "11m"}, "--ttl must be 30s to 10m"},
		{[]string{"preview", id, "3000", "--ttl", "30s"}, "--ttl must be 1m to 24h"},
		{[]string{"preview", id, "3000", "--ttl", "25h"}, "--ttl must be 1m to 24h"},
		{[]string{"mcp", "--http", "127.0.0.1:notaport"}, "--http port must be a number from 1 to 65535"},
		{[]string{"mcp", "--http", "127.0.0.1:-1"}, "--http port must be a number from 1 to 65535"},
		{[]string{"mcp", "--http", "127.0.0.1:70000"}, "--http port must be a number from 1 to 65535"},
		{[]string{"mcp", "--http", "nocolon"}, "--http must be HOST:PORT"},
		{[]string{"mcp", "--http", "0.0.0.0:8765"}, "loopback"},
		{[]string{"pause", id, "--mode", "filesystem"}, "not yet supported"},
		{[]string{"timeout", id, "0"}, "DURATION is positive"},
		{[]string{"timeout", id, "-5s"}, ""},
	}
	before := len(e.srv.Requests())
	for _, c := range cases {
		r := e.run(c.args...)
		if r.code != 2 || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%v: exit %d stderr %q, want %q", c.args, r.code, r.stderr, c.want)
		}
		if strings.Contains(r.stderr, "lookup") || strings.Contains(r.stderr, "HTTP 400") {
			t.Errorf("%v: leaked a lower-level error: %q", c.args, r.stderr)
		}
	}
	if n := len(e.srv.Requests()); n != before {
		t.Fatalf("validation made %d requests", n-before)
	}
	// In range: the preview link is minted.
	if r := e.run("preview", id, "3000", "--ttl", "15m"); r.code != 0 {
		t.Fatalf("preview 15m: exit %d %q", r.code, r.stderr)
	}
}

// timeout never removes the deadline; info says so in words.
func TestTimeoutNever(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	r := e.must("timeout", id, "never")
	if !strings.Contains(r.stderr, "no time limit") {
		t.Fatalf("timeout never: %q", r.stderr)
	}
	if sb, _ := e.srv.Sandbox(id); sb.LastTimeout != -1 {
		t.Fatalf("sent timeout %d, want -1", sb.LastTimeout)
	}
	if r := e.must("info", id); !strings.Contains(r.stdout, "never (no time limit)") {
		t.Fatalf("info: %q", r.stdout)
	}
	if r := e.must("list"); !strings.Contains(r.stdout, "never") {
		t.Fatalf("list: %q", r.stdout)
	}
	got := jsonLines(t, e.must("--json", "timeout", id, "--", "-1").stdout)
	if len(got) != 1 || got[0]["timeout"] != float64(-1) {
		t.Fatalf("timeout -- -1 --json: %v", got)
	}
	got = jsonLines(t, e.must("--json", "timeout", id, "never").stdout)
	if len(got) != 1 || got[0]["timeout"] != float64(-1) || got[0]["end_at"] != nil {
		t.Fatalf("timeout never --json: %v", got)
	}
	e.must("timeout", id, "10m")
	if sb, _ := e.srv.Sandbox(id); sb.LastTimeout != 600 {
		t.Fatalf("sent timeout %d, want 600", sb.LastTimeout)
	}
}

// The gateway reads autoPause {"idleMinutes": n} as pause-on-timeout too, so
// create --idle-pause off must not send it unless --auto-pause asked for that.
func TestCreateIdlePauseAutoPause(t *testing.T) {
	e := newEnv(t)
	cases := []struct {
		args      []string
		autoPause bool
		idle      int // -1: no idleMinutes sent
	}{
		{[]string{"--idle-pause", "off"}, false, -1},
		{[]string{"--idle-pause", "15m"}, true, 15},
		{[]string{"--auto-pause", "--idle-pause", "off"}, true, 0},
		{[]string{"--auto-pause"}, true, -1},
	}
	for _, c := range cases {
		r := e.must(append([]string{"create"}, c.args...)...)
		sb, ok := e.srv.Sandbox(strings.TrimSpace(r.stdout))
		if !ok {
			t.Fatalf("%v: no sandbox %q", c.args, r.stdout)
		}
		idle := -1
		if sb.IdleMinutes != nil {
			idle = *sb.IdleMinutes
		}
		if sb.AutoPause != c.autoPause || idle != c.idle {
			t.Errorf("%v: autoPause %v idleMinutes %d, want %v %d", c.args, sb.AutoPause, idle, c.autoPause, c.idle)
		}
	}
}

// create --timeout never (what the lifecycle docs show) sends the
// API's timeout -1 instead of failing as an invalid duration.
func TestCreateTimeoutNever(t *testing.T) {
	e := newEnv(t)
	for _, v := range []string{"never", "-1"} {
		r := e.must("create", "--timeout", v)
		id := strings.TrimSpace(r.stdout)
		sb, ok := e.srv.Sandbox(id)
		if !ok || sb.LastTimeout != -1 {
			t.Fatalf("create --timeout %s: sandbox %q sent timeout %d, want -1", v, id, sb.LastTimeout)
		}
		if r := e.must("info", id); !strings.Contains(r.stdout, "never (no time limit)") {
			t.Fatalf("info after create --timeout %s: %q", v, r.stdout)
		}
	}
	if r := e.run("create", "--timeout", "forever"); r.code != 2 || !strings.Contains(r.stderr, `"never"`) {
		t.Fatalf("create --timeout forever: exit %d %q", r.code, r.stderr)
	}
}
