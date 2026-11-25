package envd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// envd is whatever runs in the sandbox, so a redirect from it is an error and
// is never followed to another address on the caller's network.
func TestRedirectsAreNotFollowed(t *testing.T) {
	for _, code := range []int{http.StatusFound, http.StatusTemporaryRedirect} {
		for _, hc := range []*http.Client{nil, {}} {
			var hits atomic.Int32
			other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", other.URL+"/")
				w.WriteHeader(code)
			}))
			c := &Client{BaseURL: srv.URL, SandboxID: "sb", HTTP: hc}
			ctx := context.Background()
			for name, call := range map[string]func() error{
				"ReadFile":   func() error { _, err := c.ReadFile(ctx, "/x", 10); return err },
				"WriteFile":  func() error { return c.WriteFile(ctx, "/x", []byte("data")) },
				"List":       func() error { _, err := c.List(ctx); return err },
				"SendSignal": func() error { return c.SendSignal(ctx, 1, "SIGTERM") },
				"Start":      func() error { _, err := c.Start(ctx, StartRequest{}, 0); return err },
			} {
				if call() == nil {
					t.Errorf("%d %s: a redirect counted as an answer", code, name)
				}
			}
			if n := hits.Load(); n != 0 {
				t.Errorf("%d: the redirect target was contacted %d times", code, n)
			}
			srv.Close()
			other.Close()
		}
	}
}
