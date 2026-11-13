package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestTokenRefreshOn401: a dashboard token the gateway refuses (role changed, session revoked) is dropped
// and the request is sent once more, body included, with a new token; a second 401 is returned as is.
func TestTokenRefreshOn401(t *testing.T) {
	var mu sync.Mutex
	accept := "tok-2"
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Header.Get("Authorization")+" "+string(b))
		if r.Header.Get("Authorization") != "Bearer "+accept {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"code":401,"message":"session revoked: sign in again"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	n := 1
	cur := ""
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(),
		Token: func(context.Context) (string, error) {
			if cur == "" {
				cur = "tok-" + string(rune('0'+n))
				n++
			}
			return cur, nil
		},
		Refresh: func() { cur = "" },
	}
	if err := c.SetTimeout(context.Background(), "sbx", 60); err != nil {
		t.Fatalf("after refresh: %v", err)
	}
	if len(seen) != 2 || seen[0] != `Bearer tok-1 {"timeout":60}` || seen[1] != `Bearer tok-2 {"timeout":60}` {
		t.Fatalf("requests %q", seen)
	}
	mu.Lock()
	accept = "never"
	mu.Unlock()
	err := c.SetTimeout(context.Background(), "sbx", 60)
	if StatusOf(err) != http.StatusUnauthorized || len(seen) != 4 {
		t.Fatalf("still refused: %v after %d requests", err, len(seen))
	}
	// API keys are not retried.
	k := &Client{BaseURL: srv.URL, APIKey: "e2b_x", HTTP: srv.Client(), Refresh: func() { t.Fatal("refresh for an API key") }}
	if err := k.SetTimeout(context.Background(), "sbx", 1); StatusOf(err) != http.StatusUnauthorized || len(seen) != 5 {
		t.Fatalf("API key: %v, %d requests", err, len(seen))
	}
}
