package cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/fakee2b"
)

// One create that fails must not hold up or hide the others: each result is
// printed as it arrives, a summary follows, and the command exits non-zero.
func TestCreateCountOneFailureDoesNotHoldTheBatch(t *testing.T) {
	e := newEnv(t)
	// A 502 from a proxy without Should-Retry: a create never retries it.
	e.srv.InjectFault(fakee2b.Fault{Method: "POST", Path: "/v2/sandboxes", Status: 502, Times: 1})
	r := e.run("create", "--count", "3")
	if r.code == 0 {
		t.Fatalf("exit 0 with a failed create; stderr: %s", r.stderr)
	}
	if ids := strings.Fields(r.stdout); len(ids) != 2 {
		t.Fatalf("stdout = %q, want the 2 sandboxes that started", r.stdout)
	}
	if !strings.Contains(r.stderr, "created 2 of 3; 1 failed") {
		t.Errorf("stderr has no summary: %s", r.stderr)
	}
}

// A create stuck behind a busy engine is cut off at --create-timeout while
// the rest of the batch finishes; the batch does not wait for it.
func TestCreateCountTimesOutAStuckCreate(t *testing.T) {
	e := newEnv(t)
	e.srv.InjectFault(fakee2b.Fault{Method: "POST", Path: "/v2/sandboxes", Status: 503, Times: 1, Delay: 30 * time.Second})
	start := time.Now()
	r := e.run("create", "--count", "3", "--parallel", "3", "--create-timeout", "2s")
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("batch took %v; the stuck create must be cut off at 2s", took)
	}
	if r.code == 0 {
		t.Fatal("exit 0 with a timed-out create")
	}
	if ids := strings.Fields(r.stdout); len(ids) != 2 {
		t.Fatalf("stdout = %q, want 2 sandboxes", r.stdout)
	}
	if !strings.Contains(r.stderr, "timed out after 2s") {
		t.Errorf("stderr does not say the create timed out: %s", r.stderr)
	}
}

// --parallel bounds the batch.
func TestCreateCountRejectsBadParallel(t *testing.T) {
	e := newEnv(t)
	if r := e.run("create", "--count", "2", "--parallel", "0"); r.code == 0 {
		t.Fatal("--parallel 0 accepted")
	}
}

const capacityBody = `{"code":503,"error_code":"sandbox_capacity_unavailable","message":"no host has room yet"}`

// A create the gateway holds for capacity waits it out, printing progress,
// and succeeds when the capacity arrives.
func TestCreateWaitsForCapacityWithProgress(t *testing.T) {
	e := newEnv(t)
	e.srv.InjectFault(fakee2b.Fault{Method: "POST", Path: "/v2/sandboxes", Status: 503, Times: 4, Body: capacityBody,
		Header: map[string]string{"Should-Retry": "true", "Retry-After": "1"}})
	r := e.must("create")
	if ids := strings.Fields(r.stdout); len(ids) != 1 {
		t.Fatalf("stdout = %q", r.stdout)
	}
	if n := strings.Count(r.stderr, "waiting for capacity:"); n != 4 {
		t.Errorf("progress lines = %d, want 4; stderr: %s", n, r.stderr)
	}
	if !strings.Contains(r.stderr, "retrying in 1s") {
		t.Errorf("progress does not show the Retry-After: %s", r.stderr)
	}
}

// --capacity-wait 0 keeps the old short budget: 3 retries, then the error.
func TestCapacityWaitZeroUsesTheShortBudget(t *testing.T) {
	e := newEnv(t)
	e.srv.InjectFault(fakee2b.Fault{Method: "POST", Path: "/v2/sandboxes", Status: 503, Times: 10, Body: capacityBody,
		Header: map[string]string{"Should-Retry": "true", "Retry-After": "1"}})
	r := e.run("create", "--capacity-wait", "0")
	if r.code == 0 {
		t.Fatal("exit 0, want the capacity error after the short budget")
	}
	if strings.Contains(r.stderr, "waiting for capacity:") {
		t.Errorf("waited for capacity with --capacity-wait 0: %s", r.stderr)
	}
}
