package cli_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// stdout and stderr are separated and the remote exit code is ours.
func TestExecExitCodeAndStreams(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	r := e.run("exec", id, "--", "sh", "-c", "echo out; echo err >&2; exit 7")
	if r.code != 7 || r.stdout != "out\n" || r.stderr != "err\n" {
		t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	starts := e.srv.Starts()
	call := starts[len(starts)-1]
	if call.Cmd != "/bin/bash" || !slices.Equal(call.Args, []string{"-l", "-c", "sh -c 'echo out; echo err >&2; exit 7'"}) {
		t.Fatalf("start request: %q %q", call.Cmd, call.Args)
	}
	sb, _ := e.srv.Sandbox(id)
	if call.SandboxID != id || call.Port != "49983" || call.AccessToken != sb.Token {
		t.Fatalf("envd headers: %+v", call)
	}
	if call.ContentType != "application/connect+json" {
		t.Fatalf("content type %q", call.ContentType)
	}
	if call.Auth != "" {
		t.Fatalf("envd %s should get no Authorization header without -u, got %q", "0.4.2", call.Auth)
	}

	// Single argument = shell string; -w, -e and -u are passed through.
	r = e.run("exec", id, "-w", "/tmp", "-e", "GREETING=hi", "-u", "root", "--", "echo $GREETING from $(pwd)")
	if r.code != 0 || r.stdout != "hi from /tmp\n" {
		t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	call = e.srv.Starts()[len(e.srv.Starts())-1]
	if call.Cwd != "/tmp" || call.Envs["GREETING"] != "hi" || call.Auth != "Basic "+base64.StdEncoding.EncodeToString([]byte("root:")) {
		t.Fatalf("start call %+v", call)
	}

	// JSON mode: typed lines and an exit line.
	r = e.run("exec", id, "--json", "--", "echo a; echo b >&2; exit 3")
	if r.code != 3 {
		t.Fatalf("json exit %d", r.code)
	}
	lines := jsonLines(t, r.stdout)
	var sawOut, sawErr, sawExit bool
	for _, l := range lines {
		switch l["type"] {
		case "stdout":
			sawOut = l["data"] == "a\n"
		case "stderr":
			sawErr = l["data"] == "b\n"
		case "exit":
			sawExit = l["exit_code"] == float64(3)
		}
	}
	if !sawOut || !sawErr || !sawExit {
		t.Fatalf("json lines: %q", r.stdout)
	}

	// Paused sandbox: clear error, no envd call. exec passes remote codes
	// through, so its own failures are 125 (docker run's convention).
	e.srv.SetState(id, "paused")
	if r := e.run("exec", id, "--", "true"); r.code != 125 || !strings.Contains(r.stderr, "paused") {
		t.Fatalf("paused: exit %d %q", r.code, r.stderr)
	}
}

// exec and run: a remote exit code of 2, 3 or 4 must not be confused with the
// CLI's own usage, auth and not-found codes. The CLI's failures are 125 and
// --json keeps the detailed code in cli_code.
func TestExecExitCodesDoNotCollide(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	for _, code := range []int{1, 2, 3, 4, 5, 125, 255} {
		if r := e.run("exec", id, "--", "exit "+strconv.Itoa(code)); r.code != code {
			t.Errorf("remote exit %d: got %d (stderr %q)", code, r.code, r.stderr)
		}
	}
	cases := []struct {
		args    []string
		cliCode int
	}{
		{[]string{"exec", "nope", "--", "true"}, 4},
		{[]string{"exec", "only-id"}, 2},
		{[]string{"exec", id, "--no-such-flag", "--", "true"}, 2},
		{[]string{"run", "--timeout", "0s", "--", "true"}, 2},
	}
	for _, c := range cases {
		r := e.run(c.args...)
		if r.code != 125 {
			t.Errorf("%v: exit %d, want 125 (stderr %q)", c.args, r.code, r.stderr)
		}
		r = e.run(append([]string{"--json"}, c.args...)...)
		lines := jsonLines(t, r.stderr)
		if r.code != 125 || len(lines) != 1 {
			t.Fatalf("%v --json: exit %d stderr %q", c.args, r.code, r.stderr)
		}
		obj := lines[0]["error"].(map[string]any)
		if obj["exit_code"] != float64(125) || obj["cli_code"] != float64(c.cliCode) {
			t.Errorf("%v --json: error %v, want exit_code 125 cli_code %d", c.args, obj, c.cliCode)
		}
	}
	e.extra["ANONA_COMPUTE_API_KEY"] = ""
	e.extra["E2B_API_KEY"] = ""
	if r := e.run("exec", id, "--", "true"); r.code != 125 {
		t.Errorf("not signed in: exit %d", r.code)
	}
	// Other commands keep their codes.
	if r := e.run("info", id); r.code != 3 {
		t.Errorf("info not signed in: exit %d", r.code)
	}
}

// 5 MB of output arrives without loss.
func TestExecLargeOutput(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	dir := t.TempDir()
	src := dir + "/blob"
	data := make([]byte, 5<<20)
	for i := range data {
		data[i] = byte(i*7 + i/1000)
	}
	if err := os.WriteFile(src, data, 0o644); err != nil {
		t.Fatal(err)
	}
	want := sha256.Sum256(data)
	start := time.Now()
	cmd := exec.Command(binPath, "exec", id, "--", "cat "+src)
	cmd.Env = e.environ()
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	got := sha256.Sum256(out.Bytes())
	if got != want {
		t.Fatalf("sha256 %s != %s (got %d bytes)", hex.EncodeToString(got[:]), hex.EncodeToString(want[:]), out.Len())
	}
	t.Logf("5 MiB streamed in %s, sha256 %s", time.Since(start).Round(time.Millisecond), hex.EncodeToString(got[:8]))
}

// Ctrl-C sends a signal through SendSignal and the remote sleep ends.
func TestExecInterrupt(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	cmd, stdout, stderr := e.startBG("exec", id, "--", "echo ready; sleep 100")
	if line := readLineTimeout(t, stdout, 10*time.Second); line != "ready\n" {
		t.Fatalf("first line %q", line)
	}
	start := time.Now()
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	code := waitExit(t, cmd, 10*time.Second)
	if code != 130 {
		t.Fatalf("exit %d, want 130; stderr %q", code, stderr.String())
	}
	if sigs := e.srv.Signals(); len(sigs) != 1 || sigs[0] != "SIGNAL_SIGTERM" {
		t.Fatalf("signals %v", sigs)
	}
	t.Logf("remote sleep ended %s after Ctrl-C", time.Since(start).Round(time.Millisecond))
}

func TestExecBackground(t *testing.T) {
	e := newEnv(t)
	e.extra["ANONA_COMPUTE_TEST_BG_DIR"] = t.TempDir()
	id := e.srv.AddSandbox("base")
	r := e.must("exec", id, "--background", "--", "sleep 0.2")
	pid, err := strconv.Atoi(strings.TrimSpace(r.stdout))
	if err != nil || pid <= 0 {
		t.Fatalf("background stdout %q", r.stdout)
	}
	r = e.must("exec", id, "-b", "--json", "--", "sleep 0.2")
	if l := jsonLines(t, r.stdout); len(l) != 1 || l[0]["pid"] == nil {
		t.Fatalf("background json %q", r.stdout)
	}
	// Both have finished writing before the temporary directory goes.
	e.must("output", id, strconv.Itoa(pid), "-f")
	e.must("output", id, strconv.Itoa(int(jsonLines(t, r.stdout)[0]["pid"].(float64))), "-f")
}

// P-32: run creates, runs, kills and exits with the command's code.
func TestRun(t *testing.T) {
	e := newEnv(t)
	r := e.run("run", "-t", "base", "--", "sh", "-c", "echo hello; exit 5")
	if r.code != 5 || r.stdout != "hello\n" {
		t.Fatalf("run: exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "cost: unknown") || !strings.Contains(r.stderr, " deleted after ") || strings.Contains(r.stderr, "killed") {
		t.Fatalf("summary: %q", r.stderr)
	}
	if got := jsonList(t, e.must("list", "--state", "all", "--json").stdout); len(got) != 0 {
		t.Fatalf("sandbox left behind: %v", got)
	}

	r = e.run("run", "--json", "--", "echo hi")
	if r.code != 0 {
		t.Fatalf("run json: exit %d %q", r.code, r.stderr)
	}
	lines := jsonLines(t, r.stdout)
	last := lines[len(lines)-1]
	if lines[0]["type"] != "created" || last["type"] != "summary" || last["exit_code"] != float64(0) || last["cost"] != nil || last["deleted"] != true {
		t.Fatalf("run json lines %q", r.stdout)
	}
	if _, ok := last["cost"]; !ok {
		t.Fatal("summary has no cost field")
	}

	r = e.must("run", "--keep", "--", "true")
	if got := jsonList(t, e.must("list", "--json").stdout); len(got) != 1 {
		t.Fatalf("--keep: %d sandboxes", len(got))
	}
}
