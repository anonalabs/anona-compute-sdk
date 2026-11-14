package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestConnectSandboxForwardsTimeout: every timeout except 0 (no change) reaches the gateway in the body,
// including -1 (never expire); a 0 sends no body at all.
func TestConnectSandboxForwardsTimeout(t *testing.T) {
	cases := []struct {
		timeout int32
		want    string
	}{
		{-1, `{"timeout":-1}`},
		{120, `{"timeout":120}`},
		{0, ``},
	}
	for _, tc := range cases {
		var got string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			got = string(b)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"sandboxID":"sbx"}`)
		}))
		c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
		if _, _, err := c.ConnectSandbox(context.Background(), "sbx", tc.timeout); err != nil {
			srv.Close()
			t.Fatalf("timeout %d: %v", tc.timeout, err)
		}
		srv.Close()
		if got != tc.want {
			t.Errorf("timeout %d: body %q, want %q", tc.timeout, got, tc.want)
		}
	}
}

// noSleep is a stub for Client.sleep so retry tests don't actually wait.
func noSleep(context.Context, time.Duration) error { return nil }

// TestCreateSandboxRetriesColdStart503: a cold engine answers 503
// sandbox_engine_starting to the first two attempts, then 201. do's retry
// loop must retry those and CreateSandbox must succeed, having made exactly
// 3 requests.
func TestCreateSandboxRetriesColdStart503(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Should-Retry", "true") // as the gateway sends on a cold start
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"code":"sandbox_engine_starting"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"sandboxID":"sbx"}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
	sb, err := c.CreateSandbox(context.Background(), NewSandboxV2{})
	if err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if sb.SandboxID != "sbx" {
		t.Errorf("sandboxID = %q, want sbx", sb.SandboxID)
	}
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Errorf("requests = %d, want 3", got)
	}
}

// TestCreateSandboxNonRetryableStatusFailsImmediately: a 404 is not in the
// retryable set, so do must return the error after a single request.
func TestCreateSandboxNonRetryableStatusFailsImmediately(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"not found"}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
	_, err := c.CreateSandbox(context.Background(), NewSandboxV2{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("requests = %d, want 1 (no retry on 404)", got)
	}
}

// TestCreateSandboxExhaustsRetriesOnPersistent503: a 503 that never clears
// must still return an error, bounded at maxRetries retries (4 requests
// total), rather than retrying forever.
func TestCreateSandboxExhaustsRetriesOnPersistent503(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Should-Retry", "true") // as the gateway sends on a cold start
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":"sandbox_engine_starting"}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
	_, err := c.CreateSandbox(context.Background(), NewSandboxV2{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := int32(maxRetries + 1); atomic.LoadInt32(&requests) != want {
		t.Errorf("requests = %d, want %d", requests, want)
	}
}

// TestCreateSandboxHonorsRetryAfter: when the server sends Retry-After, do
// must use that delay instead of the default backoff. Verified indirectly:
// a large Retry-After would make a real-sleep test take too long, so this
// just checks the retry still happens and succeeds with sleep stubbed out.
func TestCreateSandboxHonorsRetryAfter(t *testing.T) {
	var requests int32
	var gotDelay time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		if n == 1 {
			w.Header().Set("Retry-After", "7")
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Should-Retry", "true") // as the gateway sends on a cold start
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"code":"sandbox_engine_starting"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"sandboxID":"sbx"}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	c.sleep = func(_ context.Context, d time.Duration) error { gotDelay = d; return nil }
	if _, err := c.CreateSandbox(context.Background(), NewSandboxV2{}); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if gotDelay != 7*time.Second {
		t.Errorf("sleep delay = %v, want 7s (from Retry-After)", gotDelay)
	}
}

// TestCreateSandboxShouldRetryFalseStopsImmediately: a 504 with
// Should-Retry: false means the outcome is genuinely unknown (or retrying
// cannot help), so do must not retry even though 504 is otherwise in the
// status-code retry list. Exactly 1 request must be made.
func TestCreateSandboxShouldRetryFalseStopsImmediately(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Should-Retry", "false")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = io.WriteString(w, `{"message":"engine did not respond in time"}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
	_, err := c.CreateSandbox(context.Background(), NewSandboxV2{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := atomic.LoadInt32(&requests); got != 1 {
		t.Errorf("requests = %d, want 1 (Should-Retry: false must not be retried)", got)
	}
}

// TestCreateSandboxShouldRetryTrueOverridesNonRetryableStatus: Should-Retry
// is authoritative even for a status not in the status-code retry list, so
// a 400 with Should-Retry: true must still be retried once more before
// succeeding.
func TestCreateSandboxShouldRetryTrueOverridesNonRetryableStatus(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&requests, 1)
		if n == 1 {
			w.Header().Set("Should-Retry", "true")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"message":"transient"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"sandboxID":"sbx"}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
	if _, err := c.CreateSandbox(context.Background(), NewSandboxV2{}); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
}

// TestCreateSandboxCancelDuringBackoffReturnsPromptly: canceling ctx while
// do is waiting between retries must interrupt the wait immediately rather
// than blocking out the full backoff/Retry-After duration.
func TestCreateSandboxCancelDuringBackoffReturnsPromptly(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		// A long Retry-After: if cancellation isn't honored, the test
		// would hang for this long.
		w.Header().Set("Retry-After", "30")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Should-Retry", "true") // as the gateway sends on a cold start
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":"sandbox_engine_starting"}`)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	done := make(chan error, 1)
	go func() {
		_, err := c.CreateSandbox(ctx, NewSandboxV2{})
		done <- err
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("CreateSandbox did not return promptly after context cancellation")
	}
}

// TestCreateSandboxRetryStopsAtOverallDeadline: once the overall retry
// deadline (maxRetryElapsed) has elapsed, do must stop retrying and return
// the last error even though retries remain under maxRetries and the
// status is retryable. A fake clock that jumps 25s on every call makes the
// deadline trip after 2 retries (50s, then 75s > 60s), one short of the
// maxRetries=3 bound, which is what proves the deadline -- not maxRetries
// -- is what stopped it.
func TestCreateSandboxRetryStopsAtOverallDeadline(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Should-Retry", "true") // as the gateway sends on a cold start
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"code":"sandbox_engine_starting"}`)
	}))
	defer srv.Close()

	base := time.Now()
	var calls int32
	fakeNow := func() time.Time {
		n := atomic.AddInt32(&calls, 1)
		return base.Add(time.Duration(n-1) * 25 * time.Second)
	}

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep, now: fakeNow}
	_, err := c.CreateSandbox(context.Background(), NewSandboxV2{})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	// 3 requests: the deadline trips on the 3rd check (elapsed 75s), one
	// short of the maxRetries+1 = 4 that would run without the deadline.
	if got := atomic.LoadInt32(&requests); got != 3 {
		t.Errorf("requests = %d, want 3 (deadline must stop retries before maxRetries does)", got)
	}
}

// proxyFault answers every request with status and no Should-Retry, as a
// proxy or load balancer in front of the gateway does, counting requests.
func proxyFault(t *testing.T, status int, requests *int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(requests, 1)
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, "<html><body>bad gateway</body></html>")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestNonReplayablePostIsNotRetriedOnUpstreamFailure: the decision of
// 2026-11-11. A 502, 503 or 504 without Should-Retry on a request that
// creates something (a sandbox, a fork, a template build, a preview link, a
// vault secret) may have been carried out behind the proxy that answered it,
// so it is sent exactly once and the error says the outcome is unknown.
func TestNonReplayablePostIsNotRetriedOnUpstreamFailure(t *testing.T) {
	calls := map[string]func(*Client) error{
		"create": func(c *Client) error { _, err := c.CreateSandbox(context.Background(), NewSandboxV2{}); return err },
		"fork":   func(c *Client) error { _, err := c.ForkSandbox(context.Background(), "sbx", 1, nil); return err },
		"template build": func(c *Client) error {
			_, err := c.RequestTemplateBuild(context.Background(), TemplateBuildRequest{})
			return err
		},
		"build start": func(c *Client) error {
			return c.StartDockerfileBuild(context.Background(), "tpl", "bld", DockerfileBuild{})
		},
		"preview link": func(c *Client) error { _, err := c.MintPreviewLink(context.Background(), "sbx", 8080, 60); return err },
		"vault create": func(c *Client) error {
			_, err := c.CreateVaultSecret(context.Background(), "k", "v", nil)
			return err
		},
	}
	for name, call := range calls {
		for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
			var requests int32
			srv := proxyFault(t, status, &requests)
			c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
			err := call(c)
			var apiErr *Error
			if !errors.As(err, &apiErr) || apiErr.Status != status {
				t.Fatalf("%s on %d: err = %v, want an API error with that status", name, status, err)
			}
			if !apiErr.OutcomeUnknown || !strings.Contains(err.Error(), "not retried") {
				t.Errorf("%s on %d: error %q does not say the outcome is unknown", name, status, err)
			}
			if got := atomic.LoadInt32(&requests); got != 1 {
				t.Errorf("%s on %d: requests = %d, want exactly 1", name, status, got)
			}
		}
	}
}

// TestCreateSandboxRetriesCapacity503WithShouldRetry: the gateway's 503
// sandbox_capacity_unavailable says Should-Retry: true and Retry-After, so a
// create still waits that long and retries it.
func TestCreateSandboxRetriesCapacity503WithShouldRetry(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			w.Header().Set("Should-Retry", "true")
			w.Header().Set("Retry-After", "4")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"code":503,"error_code":"sandbox_capacity_unavailable","message":"no capacity"}`)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"sandboxID":"sbx"}`)
	}))
	defer srv.Close()

	var delays []time.Duration
	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), rand: func() float64 { return 0.5 }} // 0.5: no jitter
	c.sleep = func(_ context.Context, d time.Duration) error { delays = append(delays, d); return nil }
	if _, err := c.CreateSandbox(context.Background(), NewSandboxV2{}); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
	if len(delays) != 1 || delays[0] != 4*time.Second {
		t.Errorf("waits = %v, want [4s] from Retry-After", delays)
	}
}

// TestCreateSandboxStillRetries429WithoutHeader: a 429 is a refusal made
// before any work, so even a create retries it without Should-Retry.
func TestCreateSandboxStillRetries429WithoutHeader(t *testing.T) {
	var requests int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&requests, 1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"sandboxID":"sbx"}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
	if _, err := c.CreateSandbox(context.Background(), NewSandboxV2{}); err != nil {
		t.Fatalf("CreateSandbox: %v", err)
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
}

// TestReplayableRequestsRetryUpstreamFailure: GET, DELETE, PUT and the
// replayable POSTs keep retrying a 502, 503 or 504 that has no Should-Retry,
// and their errors do not claim an unknown outcome.
func TestReplayableRequestsRetryUpstreamFailure(t *testing.T) {
	calls := map[string]func(*Client) error{
		"get":     func(c *Client) error { _, err := c.GetSandbox(context.Background(), "sbx"); return err },
		"delete":  func(c *Client) error { return c.KillSandbox(context.Background(), "sbx") },
		"put":     func(c *Client) error { _, err := c.SetIdlePause(context.Background(), "sbx", 5); return err },
		"pause":   func(c *Client) error { _, err := c.PauseSandbox(context.Background(), "sbx", true); return err },
		"resume":  func(c *Client) error { _, _, err := c.ConnectSandbox(context.Background(), "sbx", 0); return err },
		"timeout": func(c *Client) error { return c.SetTimeout(context.Background(), "sbx", 60) },
	}
	for name, call := range calls {
		for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
			var requests int32
			srv := proxyFault(t, status, &requests)
			c := &Client{BaseURL: srv.URL, HTTP: srv.Client(), sleep: noSleep}
			err := call(c)
			if err == nil {
				t.Fatalf("%s on %d: expected an error", name, status)
			}
			var apiErr *Error
			if errors.As(err, &apiErr) && apiErr.OutcomeUnknown {
				t.Errorf("%s on %d: a replayable request claims an unknown outcome: %v", name, status, err)
			}
			if got, want := atomic.LoadInt32(&requests), int32(maxRetries+1); got != want {
				t.Errorf("%s on %d: requests = %d, want %d (retried)", name, status, got, want)
			}
		}
	}
}
