package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// capacityServer answers the first n requests with the gateway's capacity
// 503 (Should-Retry: true, Retry-After: 30, error_code code) and then 201.
func capacityServer(t *testing.T, n int32, code string, shouldRetry string, requests *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(requests, 1) <= n {
			if shouldRetry != "" {
				w.Header().Set("Should-Retry", shouldRetry)
			}
			w.Header().Set("Retry-After", "30")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"code":503,"error_code":"`+code+`","message":"no capacity yet"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"sandboxID":"sbx"}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// steppingClock advances step on every call, so each retry check sees more
// time passed without a real wait.
func steppingClock(step time.Duration) func() time.Time {
	base := time.Now()
	var calls int64
	return func() time.Time {
		n := atomic.AddInt64(&calls, 1)
		return base.Add(time.Duration(n-1) * step)
	}
}

// A pool host takes 4-8 minutes to launch and warm. A create held for
// capacity must keep waiting through that, well past the ordinary 3 retries
// and 60 s budget, and succeed when the host arrives.
func TestCreateWaitsForCapacityPastTheShortBudget(t *testing.T) {
	var requests int32
	srv := capacityServer(t, 8, "sandbox_capacity_unavailable", "true", &requests)
	var waits []time.Duration
	var progress int
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), now: steppingClock(30 * time.Second)}
	c.sleep = func(_ context.Context, d time.Duration) error { waits = append(waits, d); return nil }
	c.OnCapacityWait = func(code string, next, elapsed time.Duration) {
		progress++
		if code != "sandbox_capacity_unavailable" || next != 30*time.Second {
			t.Errorf("progress(%q, %v, %v), want the capacity code and the 30s Retry-After", code, next, elapsed)
		}
	}
	sb, err := c.CreateSandbox(context.Background(), NewSandboxV2{})
	if err != nil {
		t.Fatalf("CreateSandbox: %v (a capacity hold must outlast 3 retries / 60s)", err)
	}
	if sb.SandboxID != "sbx" {
		t.Errorf("sandbox = %q", sb.SandboxID)
	}
	if got := atomic.LoadInt32(&requests); got != 9 {
		t.Errorf("requests = %d, want 9 (8 capacity holds, then the create)", got)
	}
	if len(waits) != 8 || progress != 8 {
		t.Errorf("waits = %d, progress lines = %d, want 8 each", len(waits), progress)
	}
}

// The engine waking from its idle stop is the same kind of wait.
func TestCreateWaitsForEngineStarting(t *testing.T) {
	var requests int32
	srv := capacityServer(t, 5, "sandbox_engine_starting", "true", &requests)
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep, now: steppingClock(30 * time.Second)}
	if _, err := c.CreateSandbox(context.Background(), NewSandboxV2{}); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 6 {
		t.Errorf("requests = %d, want 6", got)
	}
}

// The wait is bounded: past CapacityWait the create fails with the
// gateway's capacity error rather than waiting for ever.
func TestCapacityWaitStopsAtItsBound(t *testing.T) {
	var requests int32
	srv := capacityServer(t, 1000, "sandbox_capacity_unavailable", "true", &requests)
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep, now: steppingClock(30 * time.Second), CapacityWait: 2 * time.Minute}
	_, err := c.CreateSandbox(context.Background(), NewSandboxV2{})
	if err == nil {
		t.Fatal("expected the capacity error once the wait ran out")
	}
	if StatusOf(err) != http.StatusServiceUnavailable {
		t.Errorf("err = %v, want the 503", err)
	}
	// The clock starts at 0 and each check advances it 30s: the checks at
	// 30, 60 and 90s are inside the 2m bound and retry; at 120s none is left.
	if got := atomic.LoadInt32(&requests); got != 4 {
		t.Errorf("requests = %d, want 4", got)
	}
}

// The last wait is cut to what is left of the bound.
func TestCapacityRetryDelayNeverPassesTheBound(t *testing.T) {
	if d := capacityRetryDelay("30", 10*time.Second); d != 10*time.Second {
		t.Errorf("delay = %v, want 10s (the time left)", d)
	}
	if d := capacityRetryDelay("", time.Hour); d != 15*time.Second {
		t.Errorf("delay = %v, want the 15s default without Retry-After", d)
	}
	if d := capacityRetryDelay("30", time.Hour); d != 30*time.Second {
		t.Errorf("delay = %v, want Retry-After's 30s", d)
	}
}

// A negative CapacityWait switches the long wait off: the ordinary budget
// (at most 3 retries) applies, as before this change.
func TestCapacityWaitOff(t *testing.T) {
	var requests int32
	srv := capacityServer(t, 1000, "sandbox_capacity_unavailable", "true", &requests)
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep, CapacityWait: -1}
	if _, err := c.CreateSandbox(context.Background(), NewSandboxV2{}); err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(&requests); got != maxRetries+1 {
		t.Errorf("requests = %d, want %d (the ordinary budget)", got, maxRetries+1)
	}
}

// Only the server's explicit verdict starts a wait. A capacity code without
// Should-Retry: true is never retried for a create, because the outcome may
// be unknown (a proxy, not the gateway, may have answered).
func TestCapacityCodeWithoutShouldRetryIsNotRetried(t *testing.T) {
	var requests int32
	srv := capacityServer(t, 1000, "sandbox_capacity_unavailable", "", &requests)
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
	if _, err := c.CreateSandbox(context.Background(), NewSandboxV2{}); err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("requests = %d, want 1", got)
	}
}

// Any other retryable 503 keeps the short budget: only capacity codes wait
// for minutes.
func TestOtherRetryable503KeepsTheShortBudget(t *testing.T) {
	var requests int32
	srv := capacityServer(t, 1000, "sandbox_engine_unavailable", "true", &requests)
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
	if _, err := c.CreateSandbox(context.Background(), NewSandboxV2{}); err == nil {
		t.Fatal("expected an error")
	}
	if got := atomic.LoadInt32(&requests); got != maxRetries+1 {
		t.Errorf("requests = %d, want %d", got, maxRetries+1)
	}
}
