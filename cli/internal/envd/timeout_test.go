package envd

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A sandbox whose envd stops answering used to hang a unary call (a signal,
// a process list, an MCP tool) for ever. Timeout bounds unary calls only.
func TestTimeoutBoundsUnaryCallsOnly(t *testing.T) {
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stop) }) // runs first: release the handlers
	c := &Client{BaseURL: srv.URL, SandboxID: "sb", HTTP: &http.Client{}, Timeout: 100 * time.Millisecond}
	for name, call := range map[string]func() error{
		"List":       func() error { _, err := c.List(context.Background()); return err },
		"SendSignal": func() error { return c.SendSignal(context.Background(), 1, "SIGTERM") },
		"ListDir":    func() error { _, err := c.ListDir(context.Background(), "/", 1); return err },
	} {
		start := time.Now()
		if err := call(); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("%s: %v, want a deadline", name, err)
		}
		if d := time.Since(start); d > 5*time.Second {
			t.Errorf("%s took %s", name, d)
		}
	}
	// A stream is not cut off by Timeout: Start waits as long as the caller's
	// own context allows.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.Start(ctx, StartRequest{}, 0)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) < 350*time.Millisecond {
		t.Fatalf("Start ended after %s with %v, want it to outlive Timeout", time.Since(start), err)
	}
}
