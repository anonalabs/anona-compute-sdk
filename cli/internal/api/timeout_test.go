package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// slowServer answers every request after delay, or never (until the client
// gives up) when delay is negative.
func slowServer(t *testing.T, delay time.Duration, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		wait := time.After(delay)
		if delay < 0 {
			wait = nil
		}
		select {
		case <-wait:
			h(w, r)
		case <-r.Context().Done():
		case <-stop:
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(stop) }) // runs first: release the handlers
	return srv
}

// A gateway that accepts the connection and never answers used to hang
// `info`, `kill`, `ps` and the MCP tools for ever: nothing bounded a unary
// call unless --request-timeout was given. Timeout bounds it.
func TestUnaryCallStopsAtTimeout(t *testing.T) {
	srv := slowServer(t, -1, nil)
	c := &Client{BaseURL: srv.URL, APIKey: "k", HTTP: &http.Client{}, Timeout: 100 * time.Millisecond, sleep: noSleep}
	start := time.Now()
	_, err := c.GetSandbox(context.Background(), "sb")
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v, want a deadline", err)
	}
	if !strings.Contains(err.Error(), "no answer within 100ms") {
		t.Errorf("err %q does not say what timed out", err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("took %s", d)
	}
}

// Create, pause, resume and fork are held open by the gateway for up to two
// minutes by design; they get LifecycleTimeout, not the shorter Timeout.
func TestLifecycleCallsGetTheLongerTimeout(t *testing.T) {
	srv := slowServer(t, 300*time.Millisecond, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pause"):
			w.WriteHeader(http.StatusNoContent)
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"sandboxID":"sb"}`)
		}
	})
	c := &Client{BaseURL: srv.URL, APIKey: "k", HTTP: &http.Client{}, Timeout: 50 * time.Millisecond,
		LifecycleTimeout: 5 * time.Second, sleep: noSleep}
	if _, err := c.PauseSandbox(context.Background(), "sb", true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if _, err := c.GetSandbox(context.Background(), "sb"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("get: %v, want the 50ms Timeout to apply", err)
	}
}

// Streams and uploads are never cut off by Timeout: a build log that takes
// longer than it to arrive is read to the end, and so is a slow upload.
func TestStreamsAndUploadsIgnoreTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			_, _ = io.Copy(io.Discard, r.Body)
			time.Sleep(300 * time.Millisecond)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fl := w.(http.Flusher)
		for _, line := range []string{
			`{"type":"status","status":"building"}`,
			`{"type":"log","message":"step 1"}`,
			`{"type":"status","status":"ready"}`,
		} {
			_, _ = io.WriteString(w, line+"\n")
			fl.Flush()
			time.Sleep(150 * time.Millisecond)
		}
	}))
	t.Cleanup(srv.Close)
	c := &Client{BaseURL: srv.URL, APIKey: "k", HTTP: &http.Client{}, Timeout: 100 * time.Millisecond, sleep: noSleep}
	last, err := c.FollowTemplateBuild(context.Background(), "tpl", "bld", func(BuildEvent) {})
	if err != nil || last == nil || last.Status != "ready" {
		t.Fatalf("follow: %+v, %v", last, err)
	}
	if err := c.UploadTemplateFile(context.Background(), srv.URL+"/upload", strings.NewReader("ctx"), 3); err != nil {
		t.Fatalf("upload: %v", err)
	}
}
