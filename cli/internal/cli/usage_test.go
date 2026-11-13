package cli_test

import (
	"strings"
	"testing"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/fakee2b"
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

// TestUsageTopPages: --top N used to stop at the gateway's 50 rows ("Top sandboxes (50 of 1100)" for --top 5000).
// It now asks for up to 1000 a page and follows next_cursor until it has N rows or the period runs out.
func TestUsageTopPages(t *testing.T) {
	e := newEnv(t)
	e.srv.Configure(func(s *fakee2b.Server) { s.UsageSandboxes = 1100 })

	lines := jsonLines(t, e.must("usage", "--json", "--top", "5000").stdout)
	if len(lines) != 1 {
		t.Fatalf("usage --json --top 5000: %d lines", len(lines))
	}
	doc := lines[0]
	rows, _ := doc["sandboxes"].([]any)
	if len(rows) != 1100 || doc["sandbox_count"] != float64(1100) || doc["next_cursor"] != nil {
		t.Fatalf("usage --json --top 5000: %d rows, count %v, next %v", len(rows), doc["sandbox_count"], doc["next_cursor"])
	}
	if last := rows[1099].(map[string]any); last["sandbox_id"] != "sbx-1099" {
		t.Fatalf("last row %v", last)
	}
	var limits []string
	for _, r := range e.srv.Requests() {
		if r.Path == "/v1/usage" {
			limits = append(limits, r.Query.Get("limit")+"@"+r.Query.Get("cursor"))
		}
	}
	if strings.Join(limits, " ") != "1000@ 1000@1000" {
		t.Fatalf("pages requested: %v", limits)
	}

	// Fewer than remain: exactly N rows, and the cursor to carry on from.
	doc = jsonLines(t, e.must("usage", "--json", "--top", "1200", "--period", "2026-09").stdout)[0]
	if rows, _ := doc["sandboxes"].([]any); len(rows) != 1100 {
		t.Fatalf("--top 1200: %d rows", len(rows))
	}
	doc = jsonLines(t, e.must("usage", "--json", "--top", "1050").stdout)[0]
	if rows, _ := doc["sandboxes"].([]any); len(rows) != 1050 || doc["next_cursor"] != "1050" {
		t.Fatalf("--top 1050: %d rows, next %v", len(rows), doc["next_cursor"])
	}
	// Without --top, --json is the gateway's default page, as before.
	doc = jsonLines(t, e.must("usage", "--json").stdout)[0]
	if rows, _ := doc["sandboxes"].([]any); len(rows) != 50 || doc["next_cursor"] != "50" {
		t.Fatalf("default page: %d rows, next %v", len(rows), doc["next_cursor"])
	}

	r := e.must("usage", "--top", "1500")
	if !strings.Contains(r.stdout, "Top sandboxes (1100 of 1100)") || !strings.Contains(r.stdout, "sbx-1099") {
		t.Fatalf("usage --top 1500:\n%.400s", r.stdout)
	}

	// One sandbox's row, wherever it ranks.
	r = e.must("usage", "--sandbox", "sbx-1077")
	out := strings.Split(strings.TrimSpace(r.stdout), "\n")
	if !strings.Contains(r.stdout, "Sandbox sbx-1077:") || strings.Join(strings.Fields(out[len(out)-1])[:3], " ") != "sbx-1077 base killed" {
		t.Fatalf("usage --sandbox:\n%s", r.stdout)
	}
	if reqs := e.srv.Requests(); reqs[len(reqs)-1].Query.Get("sandboxId") != "sbx-1077" {
		t.Fatalf("request %+v", reqs[len(reqs)-1])
	}
	if r := e.must("usage", "--sandbox", "sbx-nope"); !strings.Contains(r.stdout, "Sandbox sbx-nope has no usage in 2026-10.") {
		t.Fatalf("unknown sandbox:\n%s", r.stdout)
	}
	if r := e.run("usage", "--sandbox", "x", "--top", "3"); r.code != 2 {
		t.Fatalf("--sandbox with --top: exit %d %s", r.code, r.stderr)
	}
}
