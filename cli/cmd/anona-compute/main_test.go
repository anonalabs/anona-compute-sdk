package main

import (
	"runtime/debug"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	info := func(v string) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: v}}, true
		}
	}
	none := func() (*debug.BuildInfo, bool) { return nil, false }
	for _, tc := range []struct {
		stamped string
		info    func() (*debug.BuildInfo, bool)
		want    string
	}{
		{"0.2.0", info("v0.9.0"), "0.2.0"}, // a release stamp always wins
		{"dev", info("v0.3.0"), "0.3.0"},   // go install ...@v0.3.0
		{"dev", info("v0.0.0-20261113000000-abcdef123456"), "0.0.0-20261113000000-abcdef123456"},
		{"dev", info("(devel)"), "dev"}, // go build in a checkout
		{"dev", info(""), "dev"},
		{"dev", none, "dev"},
	} {
		if got := resolveVersion(tc.stamped, tc.info); got != tc.want {
			t.Errorf("stamped %q: got %q, want %q", tc.stamped, got, tc.want)
		}
	}
}
