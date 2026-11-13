package fakee2b

import (
	"fmt"
	"net/http"
	"regexp"
	"strconv"
)

var fakePeriod = regexp.MustCompile(`^\d{4}-\d{2}$`)

// usage is a fixed GET /v1/usage document shaped like metering's UsageReport.
func (s *Server) usage(w http.ResponseWriter, r *http.Request, _ []byte) {
	period := r.URL.Query().Get("period")
	switch {
	case period == "" || period == "current":
		period = "2026-10"
	case !fakePeriod.MatchString(period):
		writeErr(w, r, http.StatusBadRequest, "period must be current or YYYY-MM")
		return
	}
	s.mu.Lock()
	n := max(s.UsageSandboxes, 1)
	s.mu.Unlock()
	all := make([]map[string]any, n)
	for i := range all {
		all[i] = map[string]any{"sandboxId": fmt.Sprintf("sbx-%d", i), "template": "base", "state": "killed", "vcpu": 2,
			"runningHours": 48 - float64(i)/100, "amountUsd": 1.2 - float64(i)/10000}
	}
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		l, err := strconv.Atoi(v)
		if err != nil || l < 1 || l > 1000 {
			writeErr(w, r, http.StatusBadRequest, "limit must be an integer from 1 to 1000")
			return
		}
		limit = l
	}
	off := 0
	if c := q.Get("cursor"); c != "" {
		o, err := strconv.Atoi(c)
		if err != nil || o < 0 {
			writeErr(w, r, http.StatusBadRequest, "cursor is not valid")
			return
		}
		off = o
	}
	page, next := []map[string]any{}, ""
	if id := q.Get("sandboxId"); id != "" {
		for _, sb := range all {
			if sb["sandboxId"] == id {
				page = append(page, sb)
			}
		}
	} else if off < n {
		end := min(off+limit, n)
		page = all[off:end]
		if end < n {
			next = strconv.Itoa(end)
		}
	}
	projected := 3.5
	doc := map[string]any{
		"tenant": map[string]any{"id": "t-1", "slug": "acme", "name": "Acme", "planId": "plan_a", "planName": "Plan A",
			"billingModel": "usage", "status": "active"},
		"period": map[string]any{"key": period, "start": period + "-01T00:00:00Z", "end": "2026-11-01T00:00:00Z",
			"asOf": "2026-10-20T10:30:00Z", "ratedThrough": "2026-10-20T10:00:00Z", "current": period == "2026-10"},
		"availablePeriods": []string{"2026-10", "2026-09"},
		"meters": []map[string]any{
			{"meter": "vcpu_hours", "unit": "vCPU-hour", "quantity": 48.25, "unitPriceUsd": 0.012, "ratedUsd": 0.576, "pendingUsd": 0.003, "amountUsd": 0.579},
			{"meter": "gib_hours", "unit": "GiB-hour", "quantity": 96.5, "unitPriceUsd": 0.008, "ratedUsd": 0.768, "pendingUsd": 0.004, "amountUsd": 0.772},
		},
		"totals": map[string]any{"ratedUsd": 1.344, "pendingUsd": 0.007, "amountUsd": 1.351, "creditUsd": 0, "dueUsd": 1.351,
			"projectedUsd": projected},
		"daily":        []any{},
		"sandboxes":    page,
		"sandboxCount": n,
		"templates":    []any{},
		"lifecycle":    map[string]any{},
	}
	if next != "" {
		doc["nextCursor"] = next
	}
	writeJSON(w, http.StatusOK, doc)
}

func (s *Server) billing(w http.ResponseWriter, _ *http.Request, _ []byte) {
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant":   map[string]any{"id": "t1", "slug": "acme", "name": "Acme", "planId": "payg", "planName": "Pay as you go", "billingModel": "usage", "status": "active"},
		"provider": "manual",
		"rates": []map[string]any{
			{"meter": "vcpu_hours", "tier": "standard", "unit": "vCPU-hour", "unitPriceUsd": 0.012, "effectiveFrom": "2026-10-01T00:00:00Z"},
			{"meter": "snapshot_gib_hours", "tier": "", "unit": "GiB-hour", "unitPriceUsd": 0.0001, "effectiveFrom": "2026-10-01T00:00:00Z", "monthlyUsd": 0.073},
		},
		"limits": map[string]any{"monthlyCreditUsd": 5, "spendCapUsd": 50, "spendCapAction": "pause", "maxConcurrentSandboxes": 20,
			"maxVcpuPerSandbox": 8, "maxMemMibPerSandbox": 16384},
		"state": map[string]any{"periodStart": "2026-10-01T00:00:00Z", "periodEnd": "2026-11-01T00:00:00Z", "ledgerUsd": 1.2, "spendUsd": 1.35,
			"state": "ok", "computedAt": "2026-10-20T10:00:00Z"},
		"invoices": []map[string]any{{"draftId": "d1", "periodStart": "2026-09-01T00:00:00Z", "periodEnd": "2026-10-01T00:00:00Z",
			"planId": "payg", "status": "draft", "ledgerUsd": 7.5, "subtotalCents": 750, "creditCents": 500, "totalCents": 250,
			"cappedTotalCents": 250, "updatedAt": "2026-10-01T01:00:00Z", "lines": []any{}}},
		"asOf": "2026-10-20T10:30:00Z",
	})
}
