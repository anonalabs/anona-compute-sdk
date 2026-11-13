package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// followServer answers the build event stream from a script: one body per
// connection, the last repeating, and records each request's query.
type followServer struct {
	mu      sync.Mutex
	bodies  []string
	status  int
	queries []string
}

func (f *followServer) start(t *testing.T) *Client {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		n := len(f.queries)
		f.queries = append(f.queries, r.URL.RawQuery)
		f.mu.Unlock()
		if f.status != 0 {
			w.WriteHeader(f.status)
			_, _ = io.WriteString(w, `{"code":404,"message":"build not found"}`)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, f.bodies[min(n, len(f.bodies)-1)])
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
}

// A stream cut while the build was still registering (a gateway restart, a
// proxy's idle cut) used to end the follow with the last status "building",
// which the CLI then reported as ready. It is reopened from the log line it
// had reached, and the follow ends with the build's real outcome and reason.
func TestFollowTemplateBuildReopensACutStream(t *testing.T) {
	f := &followServer{bodies: []string{
		`{"type":"status","status":"building","phase":"registering"}` + "\n" +
			`{"type":"log","message":"registering the template"}` + "\n" +
			`{"type":"log","message":"pulling the image 30%"}` + "\n",
		`{"type":"status","status":"error","phase":"failed","error":"registering the template failed: no healthy sandbox host is available for this request; try again shortly"}` + "\n",
	}}
	c := f.start(t)
	var events []BuildEvent
	last, err := c.FollowTemplateBuild(context.Background(), "tpl", "bld", func(ev BuildEvent) { events = append(events, ev) })
	if err != nil {
		t.Fatalf("follow: %v", err)
	}
	if last == nil || last.Status != "error" || !strings.Contains(last.Error, "no healthy sandbox host") {
		t.Fatalf("last %+v, want the error status with the engine's reason", last)
	}
	if len(events) != 4 {
		t.Errorf("%d events, want 4", len(events))
	}
	if len(f.queries) != 2 || f.queries[0] != "" || f.queries[1] != "logsOffset=2" {
		t.Fatalf("queries %q, want the reopen to resume after the 2 log lines read", f.queries)
	}
}

// A refusal is final: a missing build is not waited for.
func TestFollowTemplateBuildStopsOnARefusal(t *testing.T) {
	f := &followServer{status: http.StatusNotFound}
	c := f.start(t)
	_, err := c.FollowTemplateBuild(context.Background(), "tpl", "bld", func(BuildEvent) {})
	var ae *Error
	if !errors.As(err, &ae) || ae.Status != http.StatusNotFound {
		t.Fatalf("err %v, want the 404", err)
	}
	if len(f.queries) != 1 {
		t.Fatalf("%d requests, want 1", len(f.queries))
	}
}

// A stream that keeps closing with nothing in it is given up on, boundedly,
// and never read as the build ending.
func TestFollowTemplateBuildGivesUpOnEmptyStreams(t *testing.T) {
	f := &followServer{bodies: []string{""}}
	c := f.start(t)
	last, err := c.FollowTemplateBuild(context.Background(), "tpl", "bld", func(BuildEvent) {})
	if err == nil || !strings.Contains(err.Error(), "without a final status") {
		t.Fatalf("err %v last %+v, want a give-up", err, last)
	}
	if len(f.queries) != followReopens {
		t.Fatalf("%d requests, want %d", len(f.queries), followReopens)
	}
}
