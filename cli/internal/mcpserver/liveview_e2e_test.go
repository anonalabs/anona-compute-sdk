package mcpserver_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStdioLiveViewE2E drives get_live_view_url on a real `anona-compute mcp`
// process against a gateway whose stream host is configured, then loads the
// minted URL from that stream host in headless Chromium (a Node script
// named by ANONA_MCP_E2E_EMBED_JS): the embed page,
// the relay socket and a shell through envd in anona/base:dev.
//
//	ANONA_MCP_E2E_BIN         the anona-compute binary
//	ANONA_MCP_E2E_STREAM_URL  the stream host's public origin; enables the test
//	ANONA_MCP_E2E_API_KEY     the same key the CLI uses: mints a view-only
//	                          terminal token directly (MCP offers terminal links with control only)
//	ANONA_MCP_E2E_EMBED_JS    the script that loads the URL and reports the result
//	ANONA_MCP_E2E_OUT         screenshots and the browser result
func TestStdioLiveViewE2E(t *testing.T) {
	bin, streamURL := os.Getenv("ANONA_MCP_E2E_BIN"), os.Getenv("ANONA_MCP_E2E_STREAM_URL")
	if bin == "" || streamURL == "" {
		t.Skip("ANONA_MCP_E2E_BIN and ANONA_MCP_E2E_STREAM_URL not set")
	}
	script := os.Getenv("ANONA_MCP_E2E_EMBED_JS")
	if script == "" {
		t.Fatal("ANONA_MCP_E2E_EMBED_JS not set")
	}
	outDir := os.Getenv("ANONA_MCP_E2E_OUT")
	if outDir == "" {
		outDir = t.TempDir()
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
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "anona-liveview-e2e", Version: "0"}, nil).Connect(ctx, tr, nil)
	if err != nil {
		t.Fatalf("initialize: %v", err)
	}
	defer cs.Close()

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
		t.Logf("tools/call %s %s -> isError=%v %s", name, compact(args), res.IsError, redact(summary(res)))
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

	created := ok("create_sandbox", map[string]any{"template": "base", "timeout_seconds": 600, "metadata": map[string]any{"by": "mcp-liveview-e2e"}})
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

	fails("get_live_view_url", map[string]any{"sandbox_id": id, "mode": "terminal"}, "needs control=true")
	fails("get_live_view_url", map[string]any{"sandbox_id": "no-such-sandbox", "mode": "terminal", "control": true}, "404")

	term := ok("get_live_view_url", map[string]any{"sandbox_id": id, "mode": "terminal", "control": true, "ttl_seconds": 120})
	ctlURL := checkEmbedURL(t, term, streamURL, id, "terminal", "control", "pty", 120)
	desk := ok("get_live_view_url", map[string]any{"sandbox_id": id, "mode": "desktop"})
	checkEmbedURL(t, desk, streamURL, id, "desktop", "view", "desktop", 600)

	viewURL := ""
	if key := os.Getenv("ANONA_MCP_E2E_API_KEY"); key != "" {
		viewURL = mintDirect(ctx, t, key, id)
	}

	node := exec.CommandContext(ctx, "node", script)
	node.Env = append(os.Environ(), "MCP_EMBED_CONTROL_URL="+ctlURL, "MCP_EMBED_VIEW_URL="+viewURL, "MCP_EMBED_OUT="+outDir)
	var stdout, stderr bytes.Buffer
	node.Stdout, node.Stderr = &stdout, &stderr
	runErr := node.Run()
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	last := lines[len(lines)-1]
	_ = os.WriteFile(filepath.Join(outDir, "embed-result.json"), []byte(last+"\n"), 0o644)
	t.Logf("browser (%s) -> %s", filepath.Base(script), last)
	if runErr != nil {
		t.Fatalf("embed check failed: %v\n%s\n%s", runErr, stdout.String(), stderr.String())
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(last), &res); err != nil {
		t.Fatalf("browser result %q: %v", last, err)
	}
	if res["controlScope"] != "control" || res["controlSaw"] != "live-42 user" {
		t.Fatalf("control terminal: %v", res)
	}
	if viewURL != "" && (res["viewScope"] != "view" || res["viewSaw"] != "shared-42" || res["viewInputReachedShell"] != false) {
		t.Fatalf("view-only terminal: %v", res)
	}

	ok("kill_sandbox", map[string]any{"sandbox_id": id})
	killed = true
	fails("get_live_view_url", map[string]any{"sandbox_id": id, "mode": "terminal", "control": true}, "404")
}

// checkEmbedURL checks a get_live_view_url result: the stream host's embed
// page for the sandbox and mode, and a token in the fragment whose claims
// name the sandbox, scope and channel with the requested lifetime.
func checkEmbedURL(t *testing.T, out map[string]any, streamURL, id, mode, scope, channel string, ttl int) string {
	t.Helper()
	raw, _ := out["url"].(string)
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("url %q: %v", raw, err)
	}
	if got := u.Scheme + "://" + u.Host; got != strings.TrimRight(streamURL, "/") {
		t.Fatalf("live view URL on %s, want the stream host %s", got, streamURL)
	}
	if u.Path != "/embed/v1/"+id || u.Query().Get("mode") != mode {
		t.Fatalf("live view URL %s%s?%s", u.Host, u.Path, u.RawQuery)
	}
	if out["scope"] != scope || out["channel"] != channel {
		t.Fatalf("tool result scope/channel: %v", out)
	}
	frag, _ := url.ParseQuery(u.Fragment)
	parts := strings.Split(frag.Get("t"), ".")
	if len(parts) != 3 {
		t.Fatalf("no viewer token in the fragment of %s", u.Redacted())
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var cl struct {
		SBX string   `json:"sbx"`
		SCP string   `json:"scp"`
		CH  []string `json:"ch"`
		Exp int64    `json:"exp"`
		Iat int64    `json:"iat"`
	}
	if err := json.Unmarshal(b, &cl); err != nil {
		t.Fatal(err)
	}
	if cl.SBX != id || cl.SCP != scope || len(cl.CH) != 1 || cl.CH[0] != channel || cl.Exp-cl.Iat != int64(ttl) {
		t.Fatalf("viewer token claims %+v", cl)
	}
	exp, err := time.Parse(time.RFC3339, out["expires_at"].(string))
	if err != nil || exp.Unix() != cl.Exp {
		t.Fatalf("expires_at %v vs exp %d", out["expires_at"], cl.Exp)
	}
	return raw
}

// mintDirect mints a view-only terminal token with the gateway API (the MCP
// tool refuses view-only terminal links, since without a running shell there
// is nothing to watch; here the control page has started one).
func mintDirect(ctx context.Context, t *testing.T, key, id string) string {
	t.Helper()
	api := strings.TrimRight(os.Getenv("ANONA_COMPUTE_API_URL"), "/")
	body := `{"scope":"view","channels":["pty"],"ttlSeconds":120}`
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api+"/v1/sandboxes/"+url.PathEscape(id)+"/viewer-tokens", strings.NewReader(body))
	req.Header.Set("X-API-Key", key)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out struct {
		EmbedURL string `json:"embedUrl"`
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&out) != nil || out.EmbedURL == "" {
		t.Fatalf("view-only viewer token: status %d", res.StatusCode)
	}
	return out.EmbedURL
}

// redact drops viewer tokens from logged tool output.
func redact(s string) string {
	if i := strings.Index(s, "#t="); i >= 0 {
		j := strings.IndexAny(s[i:], `"' `)
		if j < 0 {
			return s[:i] + "#t=<redacted>"
		}
		return s[:i] + "#t=<redacted>" + s[i+j:]
	}
	return s
}
