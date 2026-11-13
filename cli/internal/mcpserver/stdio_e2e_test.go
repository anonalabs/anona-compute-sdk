package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStdioE2E drives a real `anona-compute mcp` process over stdio with the
// go-sdk client, against whatever gateway and envd the environment points
// the CLI at (ANONA_COMPUTE_CONFIG / ANONA_COMPUTE_API_URL / E2B_SANDBOX_URL ...). It runs
// only when ANONA_MCP_E2E_BIN names the anona-compute binary. Anona runs it
// twice: with an API key against a gateway with a fake engine and a real envd,
// and after a real browser login.
//
// ANONA_MCP_E2E_ROLE=viewer expects the read-only tool set and stops there.
// ANONA_MCP_E2E_FOREIGN_ID is a sandbox of another organization that every
// tool must fail to reach. ANONA_MCP_TRANSCRIPT receives the JSON-RPC wire log.
func TestStdioE2E(t *testing.T) {
	bin := os.Getenv("ANONA_MCP_E2E_BIN")
	if bin == "" {
		t.Skip("ANONA_MCP_E2E_BIN not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	var tr mcp.Transport = &mcp.CommandTransport{Command: exec.Command(bin, "mcp")}
	if p := os.Getenv("ANONA_MCP_TRANSCRIPT"); p != "" {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		tr = &mcp.LoggingTransport{Transport: tr, Writer: f}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "anona-e2e", Version: "0"}, nil)
	cs, err := client.Connect(ctx, tr, nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer cs.Close()
	init := cs.InitializeResult()
	t.Logf("initialize -> server %s %s, protocol %s", init.ServerInfo.Name, init.ServerInfo.Version, init.ProtocolVersion)

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	var names []string
	for _, tl := range tools.Tools {
		names = append(names, tl.Name)
	}
	t.Logf("tools/list -> %d tools: %s", len(names), strings.Join(names, ", "))

	call := func(name string, args map[string]any) (*mcp.CallToolResult, map[string]any) {
		t.Helper()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("%s: protocol error: %v", name, err)
		}
		var out map[string]any
		if b, err := json.Marshal(res.StructuredContent); err == nil {
			_ = json.Unmarshal(b, &out)
		}
		t.Logf("tools/call %s %s -> isError=%v %s", name, compact(args), res.IsError, summary(res))
		return res, out
	}
	ok := func(name string, args map[string]any) map[string]any {
		t.Helper()
		res, out := call(name, args)
		if res.IsError {
			t.Fatalf("%s failed: %s", name, summary(res))
		}
		return out
	}
	fails := func(name string, args map[string]any, want string) {
		t.Helper()
		res, _ := call(name, args)
		if !res.IsError || !strings.Contains(summary(res), want) {
			t.Fatalf("%s: want an error containing %q, got isError=%v %s", name, want, res.IsError, summary(res))
		}
	}

	who := ok("whoami", map[string]any{})

	if os.Getenv("ANONA_MCP_E2E_ROLE") == "viewer" {
		for _, n := range []string{"create_sandbox", "kill_sandbox", "run_command", "write_file", "pause_sandbox"} {
			if slices.Contains(names, n) {
				t.Fatalf("viewer session offers %s", n)
			}
		}
		if who["read_only"] != true || who["role"] != "viewer" {
			t.Fatalf("whoami for a viewer: %v", who)
		}
		ok("list_sandboxes", map[string]any{})
		fails("get_live_view_url", map[string]any{"sandbox_id": "x", "control": true}, "read-only")
		return
	}
	for _, n := range []string{"create_sandbox", "kill_sandbox", "pause_sandbox", "resume_sandbox", "extend_sandbox", "run_command",
		"start_process", "read_process_output", "kill_process", "read_file", "write_file", "list_files", "get_live_view_url",
		"desktop_screenshot", "get_ssh_config", "list_sandboxes", "get_sandbox", "whoami"} {
		if !slices.Contains(names, n) {
			t.Fatalf("tool %s missing", n)
		}
	}

	if foreign := os.Getenv("ANONA_MCP_E2E_FOREIGN_ID"); foreign != "" {
		fails("get_sandbox", map[string]any{"sandbox_id": foreign}, "404")
		fails("kill_sandbox", map[string]any{"sandbox_id": foreign}, "404")
		fails("run_command", map[string]any{"sandbox_id": foreign, "command": "id"}, "404")
	}

	created := ok("create_sandbox", map[string]any{"template": "base", "timeout_seconds": -1, "internet": false,
		"metadata": map[string]any{"by": "mcp-e2e"}})
	id, _ := created["sandbox_id"].(string)
	if id == "" {
		t.Fatalf("no sandbox_id: %v", created)
	}
	killed := false
	defer func() {
		if !killed {
			_, _ = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "kill_sandbox", Arguments: map[string]any{"sandbox_id": id}})
		}
	}()
	got := ok("get_sandbox", map[string]any{"sandbox_id": id})
	if got["state"] != "running" || got["end_at"] != "" {
		t.Fatalf("new no-limit sandbox: %v", got)
	}
	ext := ok("extend_sandbox", map[string]any{"sandbox_id": id, "timeout_seconds": 900})
	if ext["end_at"] == "" {
		t.Fatalf("extend kept no limit: %v", ext)
	}
	listed := ok("list_sandboxes", map[string]any{"metadata": map[string]any{"by": "mcp-e2e"}})
	if !strings.Contains(compact(listed), id) {
		t.Fatalf("list_sandboxes misses %s: %v", id, listed)
	}

	run := ok("run_command", map[string]any{"sandbox_id": id, "command": "uname -sm; echo to-stdout; echo to-stderr >&2; exit 3"})
	if run["exit_code"] != float64(3) || !strings.Contains(run["stdout"].(string), "to-stdout") || !strings.Contains(run["stderr"].(string), "to-stderr") {
		t.Fatalf("run_command: %v", run)
	}
	slow := ok("run_command", map[string]any{"sandbox_id": id, "command": "echo started; sleep 30", "timeout_seconds": 2})
	if slow["timed_out"] != true || !strings.Contains(slow["stdout"].(string), "started") {
		t.Fatalf("timeout: %v", slow)
	}

	dir := fmt.Sprintf("/tmp/mcp-e2e-%d", time.Now().UnixNano())
	body := "hello from the MCP e2e test\nline two\n"
	ok("write_file", map[string]any{"sandbox_id": id, "path": dir + "/hello.txt", "content": body})
	ok("write_file", map[string]any{"sandbox_id": id, "path": dir + "/bin.dat", "content": "AAEC/w==", "encoding": "base64"})
	read := ok("read_file", map[string]any{"sandbox_id": id, "path": dir + "/hello.txt"})
	if read["content"] != body || read["encoding"] != "text" {
		t.Fatalf("read_file: %v", read)
	}
	bin2 := ok("read_file", map[string]any{"sandbox_id": id, "path": dir + "/bin.dat"})
	if bin2["content"] != "AAEC/w==" || bin2["encoding"] != "base64" {
		t.Fatalf("read_file binary: %v", bin2)
	}
	ls := ok("list_files", map[string]any{"sandbox_id": id, "path": dir})
	if !strings.Contains(compact(ls), "hello.txt") || !strings.Contains(compact(ls), "bin.dat") {
		t.Fatalf("list_files: %v", ls)
	}
	cat := ok("run_command", map[string]any{"sandbox_id": id, "command": "wc -l < " + dir + "/hello.txt"})
	if strings.TrimSpace(cat["stdout"].(string)) != "2" {
		t.Fatalf("file written through envd not seen by a command: %v", cat)
	}
	fails("read_file", map[string]any{"sandbox_id": id, "path": dir + "/missing"}, "404")

	started := ok("start_process", map[string]any{"sandbox_id": id, "command": "for i in 1 2 3; do echo tick $i; sleep 1; done; echo done >&2"})
	pid := started["pid"]
	var outBuf, errBuf string
	so, eo := float64(0), float64(0)
	for i := 0; i < 20; i++ {
		r := ok("read_process_output", map[string]any{"sandbox_id": id, "pid": pid, "stdout_offset": so, "stderr_offset": eo, "wait_seconds": 5})
		outBuf += r["stdout"].(string)
		errBuf += r["stderr"].(string)
		so, eo = r["stdout_offset"].(float64), r["stderr_offset"].(float64)
		if r["running"] == false {
			if r["exit_code"] != float64(0) {
				t.Fatalf("background exit: %v", r)
			}
			break
		}
	}
	if outBuf != "tick 1\ntick 2\ntick 3\n" || errBuf != "done\n" {
		t.Fatalf("background output %q / %q", outBuf, errBuf)
	}
	long := ok("start_process", map[string]any{"sandbox_id": id, "command": "sleep 300"})
	ok("kill_process", map[string]any{"sandbox_id": id, "pid": long["pid"], "signal": "SIGKILL"})
	end := ok("read_process_output", map[string]any{"sandbox_id": id, "pid": long["pid"], "wait_seconds": 10})
	if end["running"] != false {
		t.Fatalf("killed process still running: %v", end)
	}

	// The base template has no X server: a clear error, not a hang.
	fails("desktop_screenshot", map[string]any{"sandbox_id": id}, "desktop")
	if os.Getenv("ANONA_MCP_E2E_DESKTOP") == "1" {
		// The envd image is the desktop one: start Xvfb and take a real screenshot.
		ok("start_process", map[string]any{"sandbox_id": id, "command": "Xvfb :7 -screen 0 800x600x24 -nolisten tcp"})
		ok("run_command", map[string]any{"sandbox_id": id, "command": "for i in $(seq 50); do xdpyinfo -display :7 >/dev/null 2>&1 && exit 0; sleep 0.2; done; exit 1"})
		res, _ := call("desktop_screenshot", map[string]any{"sandbox_id": id, "display": ":7"})
		if res.IsError || len(res.Content) == 0 {
			t.Fatalf("screenshot: %s", summary(res))
		}
		img, isImg := res.Content[0].(*mcp.ImageContent)
		if !isImg || img.MIMEType != "image/png" || len(img.Data) < 100 {
			t.Fatalf("screenshot content: %T", res.Content[0])
		}
	}

	ok("pause_sandbox", map[string]any{"sandbox_id": id})
	fails("run_command", map[string]any{"sandbox_id": id, "command": "true"}, "paused")
	ok("resume_sandbox", map[string]any{"sandbox_id": id, "timeout_seconds": 600})
	ok("run_command", map[string]any{"sandbox_id": id, "command": "test -f " + dir + "/hello.txt"})

	ok("kill_sandbox", map[string]any{"sandbox_id": id})
	killed = true
	fails("get_sandbox", map[string]any{"sandbox_id": id}, "404")
	fails("run_command", map[string]any{"sandbox_id": id, "command": "true"}, "404")
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func summary(res *mcp.CallToolResult) string {
	var parts []string
	for _, c := range res.Content {
		switch c := c.(type) {
		case *mcp.TextContent:
			s := c.Text
			if len(s) > 400 {
				s = s[:400] + "..."
			}
			parts = append(parts, s)
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image %s, %d bytes]", c.MIMEType, len(c.Data)))
		}
	}
	return strings.Join(parts, " | ")
}
