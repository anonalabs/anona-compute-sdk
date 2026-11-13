package cli_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/fakee2b"
)

var update = flag.Bool("update", false, "rewrite golden files")

const testKey = "e2b_0123456789abcdef0123456789abcdef01234567"
const testVersion = "1.2.3-test"

var binPath string

// TestMain builds the real anona-compute binary once; every test runs it as a
// subprocess.
func TestMain(m *testing.M) {
	flag.Parse()
	dir, err := os.MkdirTemp("", "anona-cli-test-")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(dir, "anona-compute")
	build := exec.Command("go", "build", "-ldflags", "-X main.version="+testVersion, "-o", binPath, "../../cmd/anona-compute")
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

type env struct {
	t      *testing.T
	srv    *fakee2b.Server
	url    string
	cfgDir string
	extra  map[string]string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	srv := fakee2b.New(testKey)
	hs := httptest.NewServer(srv.Handler())
	t.Cleanup(hs.Close)
	return &env{t: t, srv: srv, url: hs.URL, cfgDir: t.TempDir(), extra: map[string]string{}}
}

func (e *env) environ() []string {
	vars := map[string]string{
		"PATH":                  os.Getenv("PATH"),
		"HOME":                  e.cfgDir,
		"XDG_CONFIG_HOME":       e.cfgDir,
		"ANONA_COMPUTE_API_URL": e.url,
		"ANONA_COMPUTE_API_KEY": testKey,
		// The SDK's envd URL override; the fake serves envd on the same port.
		"E2B_SANDBOX_URL": e.url,
	}
	for k, v := range e.extra {
		if v == "" {
			delete(vars, k)
		} else {
			vars[k] = v
		}
	}
	var out []string
	for k, v := range vars {
		out = append(out, k+"="+v)
	}
	return out
}

type result struct {
	stdout, stderr string
	code           int
}

func (e *env) runIn(stdin string, args ...string) result {
	e.t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = e.environ()
	if stdin != "" {
		// An empty stdin stays /dev/null: a pipe would make exec and run
		// warn that stdin is ignored without -i.
		cmd.Stdin = strings.NewReader(stdin)
	}
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		e.t.Fatalf("run %v: %v", args, err)
	}
	return result{so.String(), se.String(), code}
}

func (e *env) run(args ...string) result {
	e.t.Helper()
	return e.runIn("", args...)
}

// must runs and requires exit 0.
func (e *env) must(args ...string) result {
	e.t.Helper()
	r := e.run(args...)
	if r.code != 0 {
		e.t.Fatalf("anona-compute %s: exit %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), r.code, r.stdout, r.stderr)
	}
	return r
}

// jsonLines parses JSONL output; every non-empty line must be one object.
func jsonLines(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(strings.NewReader(s))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line is not a JSON object: %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// startBG starts the binary in the background with pipes for output.
func (e *env) startBG(args ...string) (*exec.Cmd, *bufio.Reader, *bytes.Buffer) {
	e.t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Env = e.environ()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		e.t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(func() { _ = cmd.Process.Kill() })
	return cmd, bufio.NewReader(stdout), &stderr
}

func readLineTimeout(t *testing.T, r *bufio.Reader, d time.Duration) string {
	t.Helper()
	ch := make(chan string, 1)
	go func() {
		line, _ := r.ReadString('\n')
		ch <- line
	}()
	select {
	case l := <-ch:
		return l
	case <-time.After(d):
		t.Fatalf("no output line within %s", d)
		return ""
	}
}

func waitExit(t *testing.T, cmd *exec.Cmd, d time.Duration) int {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		if err != nil {
			t.Fatal(err)
		}
		return 0
	case <-time.After(d):
		t.Fatalf("process did not exit within %s", d)
		return -1
	}
}
