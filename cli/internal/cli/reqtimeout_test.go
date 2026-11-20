package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"
)

// clientFor parses args as the CLI would and returns the API and envd clients
// the command would use.
func clientFor(t *testing.T, args ...string) (unary, lifecycle, envdUnary time.Duration) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", dir)
	env := map[string]string{"ANONA_COMPUTE_API_KEY": "e2b_0123456789abcdef0123456789abcdef01234567", "ANONA_COMPUTE_API_URL": "http://127.0.0.1:1"}
	a := &app{stdin: strings.NewReader(""), stdout: &bytes.Buffer{}, stderr: &bytes.Buffer{}, getenv: func(k string) string {
		if v, ok := env[k]; ok {
			return v
		}
		return os.Getenv(k)
	}}
	root := newRoot(a)
	if err := root.PersistentFlags().Parse(args); err != nil {
		t.Fatal(err)
	}
	cl, s, err := a.client()
	if err != nil {
		t.Fatal(err)
	}
	ec := a.envdClient(s, "sb", "", "0.5.0", nil, "")
	if cl.HTTP.Timeout != 0 || ec.HTTP.Timeout != 0 {
		t.Fatalf("an http.Client Timeout (%s, %s) would cut off streams and uploads", cl.HTTP.Timeout, ec.HTTP.Timeout)
	}
	return cl.Timeout, cl.LifecycleTimeout, ec.Timeout
}

// Without --request-timeout a non-streaming call used to have no limit at
// all, so `info`, `kill`, `ps` and the MCP tools hung for ever on a gateway
// or sandbox that stopped answering. They now default to 60 s, with the long
// lifecycle calls given 3 minutes; an explicit value applies to every unary
// call and 0 still means none.
func TestRequestTimeoutDefaults(t *testing.T) {
	for _, c := range []struct {
		args                []string
		unary, life, envdTO time.Duration
	}{
		{nil, time.Minute, 3 * time.Minute, time.Minute},
		{[]string{"--request-timeout", "5s"}, 5 * time.Second, 5 * time.Second, 5 * time.Second},
		{[]string{"--request-timeout", "0"}, 0, 0, 0},
	} {
		u, l, e := clientFor(t, c.args...)
		if u != c.unary || l != c.life || e != c.envdTO {
			t.Errorf("%v: unary %s lifecycle %s envd %s, want %s %s %s", c.args, u, l, e, c.unary, c.life, c.envdTO)
		}
	}
}
