package cli_test

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/fakee2b"
)

// 100 lines with --since 1h, and --follow shows new lines quickly.
func TestLogs(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	base := time.Now().Add(-30 * time.Minute)
	for i := 0; i < 100; i++ {
		// Several lines share a millisecond, to exercise the cursor.
		e.srv.AddLog(id, fakee2b.LogEntry{Timestamp: base.Add(time.Duration(i/3) * time.Millisecond), Level: "info", Message: fmt.Sprintf("line %03d", i)})
	}
	e.srv.AddLog(id, fakee2b.LogEntry{Timestamp: time.Now().Add(-2 * time.Hour), Level: "info", Message: "too old"})

	r := e.must("logs", id, "--since", "1h")
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if len(lines) != 100 || !strings.HasSuffix(lines[0], "line 000") || !strings.HasSuffix(lines[99], "line 099") {
		t.Fatalf("got %d lines: first %q", len(lines), lines[0])
	}
	q := e.srv.Requests()[len(e.srv.Requests())-1]
	if q.Path != "/v2/sandboxes/"+id+"/logs" || q.Query.Get("direction") != "forward" || q.Query.Get("cursor") == "" {
		t.Fatalf("logs request %s %v", q.Path, q.Query)
	}
	if got := jsonLines(t, e.must("logs", id, "--json").stdout); len(got) != 101 {
		t.Fatalf("json logs: %d", len(got))
	}
	e.srv.AddLog(id, fakee2b.LogEntry{Timestamp: time.Now().Add(-30 * time.Second), Level: "error", Message: "boom"})
	if got := jsonLines(t, e.must("logs", id, "--level", "error", "--json").stdout); len(got) != 1 || got[0]["message"] != "boom" {
		t.Fatalf("level filter: %v", got)
	}

	// Follow.
	cmd, stdout, stderr := e.startBG("logs", id, "--since", "1m", "--follow")
	if l := readLineTimeout(t, stdout, 5*time.Second); !strings.HasSuffix(l, "boom\n") {
		t.Fatalf("first follow line %q", l)
	}
	for i := 0; i < 3; i++ {
		msg := fmt.Sprintf("new %d", i)
		emitted := time.Now()
		e.srv.AddLog(id, fakee2b.LogEntry{Timestamp: emitted, Level: "info", Message: msg})
		l := readLineTimeout(t, stdout, 2*time.Second)
		lag := time.Since(emitted)
		if !strings.HasSuffix(l, msg+"\n") {
			t.Fatalf("follow line %q, want %q", l, msg)
		}
		if lag > time.Second {
			t.Fatalf("line %d arrived after %s", i, lag)
		}
		t.Logf("follow: %q after %s", msg, lag.Round(time.Millisecond))
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	if code := waitExit(t, cmd, 5*time.Second); code != 0 {
		t.Fatalf("follow exit %d after Ctrl-C; stderr %q", code, stderr.String())
	}
}

func TestLogsFollowStopsWhenSandboxEnds(t *testing.T) {
	e := newEnv(t)
	id := e.srv.AddSandbox("base")
	e.srv.AddLog(id, fakee2b.LogEntry{Timestamp: time.Now(), Level: "info", Message: "hello"})
	cmd, stdout, _ := e.startBG("logs", id, "-f")
	readLineTimeout(t, stdout, 5*time.Second)
	e.must("kill", id)
	if code := waitExit(t, cmd, 6*time.Second); code != 0 {
		t.Fatalf("exit %d", code)
	}
}
