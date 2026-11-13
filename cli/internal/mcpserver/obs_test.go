package mcpserver_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/fakee2b"
)

// MCP-OBS: get_sandbox_logs returns the newest lines oldest-first, with the
// stream a process line came from, and get_sandbox_metrics the latest sample.
func TestSandboxLogsAndMetricsTools(t *testing.T) {
	cs, fake := connect(t, true)
	id := fake.AddSandbox("base")
	base := time.Now().Add(-time.Minute).UTC()
	for i := 0; i < 5; i++ {
		fake.AddLog(id, fakee2b.LogEntry{Timestamp: base.Add(time.Duration(i) * time.Second), Level: "info",
			Message: "out " + string(rune('A'+i)), Fields: map[string]string{"stream": "stdout"}})
	}
	fake.AddLog(id, fakee2b.LogEntry{Timestamp: base.Add(6 * time.Second), Level: "error", Message: "boom",
		Fields: map[string]string{"stream": "stderr"}})

	out, res := call(t, cs, "get_sandbox_logs", map[string]any{"sandbox_id": id, "limit": 3})
	if res.IsError {
		t.Fatalf("get_sandbox_logs: %v", res.Content)
	}
	var logs struct {
		SandboxID string `json:"sandbox_id"`
		Lines     []struct {
			Timestamp, Level, Message, Stream string
		} `json:"lines"`
		Truncated bool `json:"truncated"`
	}
	b, _ := json.Marshal(out)
	if err := json.Unmarshal(b, &logs); err != nil {
		t.Fatal(err)
	}
	if len(logs.Lines) != 3 || logs.SandboxID != id || !logs.Truncated {
		t.Fatalf("logs out %+v", logs)
	}
	if logs.Lines[2].Message != "boom" || logs.Lines[2].Level != "error" || logs.Lines[2].Stream != "stderr" {
		t.Fatalf("newest line last: %+v", logs.Lines)
	}
	if logs.Lines[0].Timestamp == "" {
		t.Fatalf("no timestamp: %+v", logs.Lines[0])
	}
	// The request asked the gateway for the newest lines.
	q := fake.Requests()[len(fake.Requests())-1]
	if q.Path != "/v2/sandboxes/"+id+"/logs" || q.Query.Get("direction") != "backward" || q.Query.Get("limit") != "3" {
		t.Fatalf("logs request %s %v", q.Path, q.Query)
	}
	// A level filter reaches the gateway.
	call(t, cs, "get_sandbox_logs", map[string]any{"sandbox_id": id, "level": "error", "search": "boom"})
	q = fake.Requests()[len(fake.Requests())-1]
	if q.Query.Get("level") != "error" || q.Query.Get("search") != "boom" {
		t.Fatalf("filters not sent: %v", q.Query)
	}

	now := time.Now().UTC()
	fake.AddMetric(id, fakee2b.Metric{Timestamp: now.Add(-time.Second), TimestampUnix: now.Add(-time.Second).Unix(),
		CPUCount: 2, CPUUsedPct: 3, MemUsed: 1 << 20, MemTotal: 1 << 30, DiskUsed: 1 << 21, DiskTotal: 1 << 31})
	fake.AddMetric(id, fakee2b.Metric{Timestamp: now, TimestampUnix: now.Unix(),
		CPUCount: 2, CPUUsedPct: 44.5, MemUsed: 512 << 20, MemTotal: 1 << 30, DiskUsed: 2 << 21, DiskTotal: 1 << 31})
	out, res = call(t, cs, "get_sandbox_metrics", map[string]any{"sandbox_id": id})
	if res.IsError {
		t.Fatalf("get_sandbox_metrics: %v", res.Content)
	}
	var m struct {
		SandboxID  string  `json:"sandbox_id"`
		CPUCount   int     `json:"cpu_count"`
		CPUUsedPct float64 `json:"cpu_used_pct"`
		MemUsed    int64   `json:"mem_used_bytes"`
		MemTotal   int64   `json:"mem_total_bytes"`
		DiskTotal  int64   `json:"disk_total_bytes"`
		Samples    int     `json:"samples"`
		TakenAt    string  `json:"taken_at"`
	}
	b, _ = json.Marshal(out)
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.SandboxID != id || m.Samples != 2 || m.CPUUsedPct != 44.5 || m.MemUsed != 512<<20 || m.MemTotal != 1<<30 ||
		m.DiskTotal != 1<<31 || m.CPUCount != 2 || m.TakenAt == "" {
		t.Fatalf("metrics out %+v", m)
	}
	t.Logf("get_sandbox_metrics -> %+v", m)
	// A sandbox with no samples is not an error.
	empty := fake.AddSandbox("base")
	out, res = call(t, cs, "get_sandbox_metrics", map[string]any{"sandbox_id": empty})
	if res.IsError || out["samples"].(float64) != 0 {
		t.Fatalf("no samples: %v %v", out, res.Content)
	}
}
