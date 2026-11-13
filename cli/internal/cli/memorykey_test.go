package cli_test

import (
	"strings"
	"testing"
)

// A non-Compute key in ANONA_API_KEY (Anona Memory's) is never used.
func TestMemoryKeyIgnored(t *testing.T) {
	e := newEnv(t)
	e.extra["ANONA_COMPUTE_API_KEY"] = ""
	e.extra["ANONA_API_KEY"] = "anona_memory_key_123"
	if r := e.run("list"); r.code != 3 {
		t.Fatalf("memory key used: exit %d %s", r.code, r.stderr)
	}
	for _, req := range e.srv.Requests() {
		if strings.Contains(req.Header.Get("X-API-Key")+req.Header.Get("Authorization"), "anona_memory") {
			t.Fatal("memory key sent to the gateway")
		}
	}
	e.extra["ANONA_API_KEY"] = testKey
	e.must("list")
}
