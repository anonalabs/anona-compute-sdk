package mcpserver_test

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPreviewURL(t *testing.T) {
	cs, fake := connect(t, false)
	out, res := call(t, cs, "create_sandbox", map[string]any{"template": "base"})
	if res.IsError {
		t.Fatalf("create: %v", res.Content)
	}
	id := out["sandbox_id"].(string)
	out, res = call(t, cs, "get_preview_url", map[string]any{"sandbox_id": id, "port": 3000, "ttl_seconds": 900})
	if res.IsError || !strings.HasPrefix(out["url"].(string), "https://3000-"+id+".") || out["port"] != float64(3000) || out["expires_at"] == "" {
		t.Fatalf("get_preview_url: %v %v", out, res.Content)
	}
	var body map[string]any
	for _, r := range fake.Requests() {
		if r.Method == "POST" && strings.HasSuffix(r.Path, "/preview-links") {
			_ = json.Unmarshal([]byte(r.Body), &body)
		}
	}
	if body["port"] != float64(3000) || body["ttlSeconds"] != float64(900) {
		t.Fatalf("gateway request %v", body)
	}
	if _, res := call(t, cs, "get_preview_url", map[string]any{"sandbox_id": id, "port": 70000}); !res.IsError {
		t.Fatal("port 70000 accepted")
	}
	if _, res := call(t, cs, "get_preview_url", map[string]any{"sandbox_id": "nope", "port": 3000}); !res.IsError {
		t.Fatal("unknown sandbox accepted")
	}
	ro, _ := connect(t, true)
	for _, n := range toolNames(t, ro) {
		if n == "get_preview_url" {
			t.Fatal("read-only session offers get_preview_url")
		}
	}
}
