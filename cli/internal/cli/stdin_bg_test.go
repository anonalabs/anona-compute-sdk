package cli_test

import (
	"bytes"
	"crypto/sha256"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// exec -i streams stdin to the remote process in order and closes it at EOF.
func TestExecStdin(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	data := bytes.Repeat([]byte("0123456789abcdef\n"), 40000) // 680 KB, many SendInput calls
	cmd := exec.Command(binPath, "exec", "-i", id, "--", "sha256sum")
	cmd.Env = e.environ()
	cmd.Stdin = bytes.NewReader(data)
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("%v: %s", err, stderr.String())
	}
	sum := sha256.Sum256(data)
	if got := strings.Fields(out.String()); len(got) == 0 || got[0] != hexString(sum[:]) {
		t.Fatalf("remote sha256 %q, want %x", out.String(), sum)
	}
	if starts := e.srv.Starts(); !starts[len(starts)-1].Stdin {
		t.Fatal("exec -i must start the process with stdin")
	}

	// run -i too.
	r := e.runIn("hello from stdin\n", "run", "-i", "-t", "base", "--", "cat")
	if r.code != 0 || r.stdout != "hello from stdin\n" {
		t.Fatalf("run -i: exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
}

// Without -i, piped stdin is not sent, and the CLI says so instead of
// dropping it silently. A terminal or /dev/null gets no warning.
func TestExecStdinIgnoredWarns(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	r := e.runIn("some input\n", "exec", id, "--", "cat; echo done")
	if r.code != 0 || r.stdout != "done\n" {
		t.Fatalf("exit %d stdout %q stderr %q", r.code, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr, "stdin is not sent to the command without -i/--stdin") {
		t.Fatalf("no warning: %q", r.stderr)
	}
	if starts := e.srv.Starts(); starts[len(starts)-1].Stdin {
		t.Fatal("exec without -i must start the process without stdin")
	}
	if r := e.run("exec", id, "--", "true"); strings.Contains(r.stderr, "warning") {
		t.Fatalf("/dev/null stdin warned: %q", r.stderr)
	}
	if r := e.runIn("x", "exec", "-b", "-i", id, "--", "true"); r.code != 125 || !strings.Contains(r.stderr, "--stdin cannot be used with --background") {
		t.Fatalf("-b -i: exit %d %q", r.code, r.stderr)
	}
}

func hexString(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, digits[c>>4], digits[c&15])
	}
	return string(out)
}

// exec -b keeps the command's output in the sandbox; output reads it, -f
// follows it to the end, and ps lists the process while it runs.
func TestExecBackgroundOutput(t *testing.T) {
	e := newEnv(t)
	e.extra["ANONA_COMPUTE_TEST_BG_DIR"] = t.TempDir()
	id := e.srv.AddSandbox("base")

	r := e.must("exec", id, "-b", "--", "echo out1; echo err1 >&2; sleep 0.6; echo out2; exit 3")
	pid := strings.TrimSpace(r.stdout)
	if _, err := strconv.Atoi(pid); err != nil {
		t.Fatalf("background stdout %q", r.stdout)
	}
	if want := "anona-compute output " + id + " " + pid; !strings.Contains(r.stderr, want) {
		t.Fatalf("no hint %q in %q", want, r.stderr)
	}

	// While it runs: ps shows it, output shows what it wrote so far.
	time.Sleep(150 * time.Millisecond)
	ps := e.must("ps", id)
	if !strings.Contains(ps.stdout, pid) || !strings.Contains(ps.stdout, "saved") || !strings.Contains(ps.stdout, "sleep 0.6") || strings.Contains(ps.stdout, "anona-compute background") {
		t.Fatalf("ps: %q", ps.stdout)
	}
	psJSON := jsonLines(t, e.must("ps", id, "--json").stdout)
	found := false
	for _, p := range psJSON {
		if strconv.Itoa(int(p["pid"].(float64))) == pid && p["background"] == true {
			found = true
		}
	}
	if !found {
		t.Fatalf("ps --json: %v", psJSON)
	}
	r = e.must("output", id, pid)
	if r.stdout != "out1\n" || !strings.HasPrefix(r.stderr, "err1\n") || !strings.Contains(r.stderr, "still running") {
		t.Fatalf("output while running: stdout %q stderr %q", r.stdout, r.stderr)
	}

	// -f follows until the process ends, then reports its exit code.
	r = e.must("output", id, pid, "-f")
	if r.stdout != "out1\nout2\n" || !strings.Contains(r.stderr, "err1") || !strings.Contains(r.stderr, "exited with code 3") {
		t.Fatalf("output -f: stdout %q stderr %q", r.stdout, r.stderr)
	}
	got := jsonLines(t, e.must("output", id, pid, "--json").stdout)
	if len(got) != 1 || got[0]["stdout"] != "out1\nout2\n" || got[0]["stderr"] != "err1\n" || got[0]["state"] != "exited" || got[0]["exit_code"] != float64(3) {
		t.Fatalf("output --json: %v", got)
	}

	// A PID without saved output is not found (4); a bad PID is a usage error.
	if r := e.run("output", id, "999999"); r.code != 4 || !strings.Contains(r.stderr, "exec -b") {
		t.Fatalf("unknown pid: exit %d %q", r.code, r.stderr)
	}
	if r := e.run("output", id, "abc"); r.code != 2 {
		t.Fatalf("bad pid: exit %d", r.code)
	}
	// A single simple command: bash must not exec it directly (that would
	// skip the EXIT trap that records the status).
	pid = strings.TrimSpace(e.must("exec", id, "-b", "--", "sh", "-c", "exit 4").stdout)
	r = e.must("output", id, pid, "-f")
	if !strings.Contains(r.stderr, "exited with code 4") {
		t.Fatalf("simple command status: %q", r.stderr)
	}

	r = e.must("exec", id, "-b", "--json", "--", "true")
	if l := jsonLines(t, r.stdout); len(l) != 1 || l[0]["pid"] == nil || !strings.HasPrefix(l[0]["output_command"].(string), "anona-compute output "+id+" ") {
		t.Fatalf("background json %q", r.stdout)
	}
	// Let it finish before the temporary directory is removed.
	e.must("output", id, strconv.Itoa(int(jsonLines(t, r.stdout)[0]["pid"].(float64))), "-f")
}
