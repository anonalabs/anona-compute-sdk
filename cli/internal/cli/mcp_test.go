package cli_test

import (
	"bufio"
	"context"
	"net/http"
	"os/exec"
	"regexp"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type bearerRT struct{ token string }

func (b bearerRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	if b.token != "" {
		r.Header.Set("Authorization", "Bearer "+b.token)
	}
	return http.DefaultTransport.RoundTrip(r)
}

// `anona-compute mcp --http`: streamable HTTP on loopback, bearer token required.
func TestMCPHTTP(t *testing.T) {
	e := newEnv(t)
	if r := e.run("mcp", "--http", "0.0.0.0:0"); r.code != 2 {
		t.Fatalf("non-loopback address: exit %d %s", r.code, r.stderr)
	}
	cmd := exec.Command(binPath, "mcp", "--http", "127.0.0.1:0")
	cmd.Env = e.environ()
	stderr, _ := cmd.StderrPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	sc := bufio.NewScanner(stderr)
	var url, token string
	deadline := time.Now().Add(20 * time.Second)
	for (url == "" || token == "") && time.Now().Before(deadline) && sc.Scan() {
		if m := regexp.MustCompile(`on (http://\S+/mcp)`).FindStringSubmatch(sc.Text()); m != nil {
			url = m[1]
		}
		if m := regexp.MustCompile(`Bearer (\S+)`).FindStringSubmatch(sc.Text()); m != nil {
			token = m[1]
		}
	}
	if url == "" || token == "" {
		t.Fatalf("no address or token printed (url %q)", url)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Without the token: refused.
	res, err := http.Post(url, "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: %d", res.StatusCode)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	cs, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: url, HTTPClient: &http.Client{Transport: bearerRT{token}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	r, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_sandboxes", Arguments: map[string]any{}})
	if err != nil || r.IsError {
		t.Fatalf("list_sandboxes over HTTP: %v %v", err, r)
	}
}
