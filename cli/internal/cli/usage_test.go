package cli_test

import (
	"strings"
	"testing"
)

func TestUsageCommand(t *testing.T) {
	e := newEnv(t)
	r := e.must("usage")
	for _, want := range []string{"Acme, plan Plan A, period 2026-10 (current, as of 2026-10-20 10:30 UTC)", "vcpu_hours", "48.250",
		"Total   $1.3510 (rated $1.3440 + not yet rated $0.0070)", "Projected for the month $3.50", "Top sandboxes (1 of 1)", "sbx-0"} {
		if !strings.Contains(r.stdout, want) {
			t.Fatalf("usage output misses %q:\n%s", want, r.stdout)
		}
	}
	lines := jsonLines(t, e.must("usage", "--period", "2026-09", "--json").stdout)
	if len(lines) != 1 || lines[0]["period"].(map[string]any)["key"] != "2026-09" || lines[0]["daily"] == nil {
		t.Fatalf("usage --json: %v", lines)
	}
	reqs := e.srv.Requests()
	if last := reqs[len(reqs)-1]; last.Path != "/v1/usage" || last.Query.Get("period") != "2026-09" || last.Header.Get("X-API-Key") != testKey {
		t.Fatalf("request %+v", last)
	}
	if b := e.must("usage", "--billing", "--json"); !strings.Contains(b.stdout, `"provider":"manual"`) || !strings.Contains(b.stdout, `"unit_price_usd":0.012`) {
		t.Fatalf("usage --billing --json: %s", b.stdout)
	}
	// Text mode is readable text, not a JSON dump.
	b := e.must("usage", "--billing")
	for _, want := range []string{"Acme, plan Pay as you go (usage billing), invoices: manual", "Monthly credit  $5.00",
		"Spend cap       $50.00 (then pause)", "20 sandboxes at once, 8 vCPU and 16384 MiB per sandbox",
		"Rates:", "vcpu_hours", "$0.012000", "$0.0730", "Invoices:", "2026-09", "draft", "$7.50", "$5.00", "$2.50"} {
		if !strings.Contains(b.stdout, want) {
			t.Errorf("usage --billing misses %q:\n%s", want, b.stdout)
		}
	}
	if strings.Contains(b.stdout, "{") {
		t.Errorf("usage --billing printed JSON:\n%s", b.stdout)
	}
	if r := e.run("usage", "--period", "last-week"); r.code != 2 || !strings.Contains(r.stderr, "--period must be current or YYYY-MM") {
		t.Fatalf("bad period: %d %s", r.code, r.stderr)
	}
	e.extra["ANONA_COMPUTE_API_KEY"] = "e2b_ffff"
	if r := e.run("usage"); r.code != 3 {
		t.Fatalf("wrong key: exit %d %s", r.code, r.stderr)
	}
}
