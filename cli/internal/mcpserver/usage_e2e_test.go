package mcpserver_test

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestStdioUsageE2E calls get_usage on a real `anona-compute mcp` process whose gateway proxies the real
// metering read API:
//
//	ANONA_MCP_E2E_BIN           the anona-compute binary
//	ANONA_MCP_E2E_USAGE_PERIOD  the period holding the seeded usage (YYYY-MM); enables the test
//	ANONA_MCP_E2E_USAGE_VCPU    the vCPU-hours seeded in it
//	ANONA_MCP_E2E_USAGE_AMOUNT  metering's own total for it (read directly), which the tool must return
func TestStdioUsageE2E(t *testing.T) {
	bin, period := os.Getenv("ANONA_MCP_E2E_BIN"), os.Getenv("ANONA_MCP_E2E_USAGE_PERIOD")
	if bin == "" || period == "" {
		t.Skip("ANONA_MCP_E2E_BIN and ANONA_MCP_E2E_USAGE_PERIOD not set")
	}
	wantVCPU, _ := strconv.ParseFloat(os.Getenv("ANONA_MCP_E2E_USAGE_VCPU"), 64)
	wantAmount, _ := strconv.ParseFloat(os.Getenv("ANONA_MCP_E2E_USAGE_AMOUNT"), 64)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "anona-usage-e2e", Version: "0"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: exec.Command(bin, "mcp", "--read-only")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_usage", Arguments: map[string]any{"period": period}})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("get_usage: %s", summary(res))
	}
	var out struct {
		Period struct{ Key string } `json:"period"`
		Meters []struct {
			Meter     string  `json:"meter"`
			Quantity  float64 `json:"quantity"`
			AmountUSD float64 `json:"amountUsd"`
		} `json:"meters"`
		Totals struct {
			AmountUSD float64 `json:"amountUsd"`
		} `json:"totals"`
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	var vcpu float64
	for _, m := range out.Meters {
		if m.Meter == "vcpu_hours" {
			vcpu = m.Quantity
		}
	}
	t.Logf("get_usage period=%s -> %d meters, vcpu_hours %.4f, total $%.6f", out.Period.Key, len(out.Meters), vcpu, out.Totals.AmountUSD)
	if out.Period.Key != period || math.Abs(vcpu-wantVCPU) > 1e-9 || math.Abs(out.Totals.AmountUSD-wantAmount) > 1e-9 || wantAmount <= 0 {
		t.Fatalf("get_usage: period %s vcpu %v total %v, want %s %v %v", out.Period.Key, vcpu, out.Totals.AmountUSD, period, wantVCPU, wantAmount)
	}
	if _, ok := res.StructuredContent.(map[string]any)["daily"]; ok {
		t.Fatal("daily returned without include_daily")
	}
}
