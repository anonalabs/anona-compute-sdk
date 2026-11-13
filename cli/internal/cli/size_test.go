package cli

import (
	"strings"
	"testing"
)

func TestCreateSizeFlag(t *testing.T) {
	for in, want := range map[string]string{"small": "small", "M": "medium", "large": "large", "XL": "xl", "xlarge": "xl"} {
		o := &createOpts{size: in, timeout: "300s"}
		req, err := o.request("base")
		if err != nil || req.Size != want {
			t.Errorf("--size %s: %q %v, want %s", in, req.Size, err, want)
		}
	}
	o := &createOpts{size: "huge", timeout: "300s"}
	if _, err := o.request("base"); err == nil || !strings.Contains(err.Error(), "small, medium, large or xl") {
		t.Fatalf("--size huge: %v", err)
	}
	o = &createOpts{timeout: "300s"}
	if req, err := o.request("base"); err != nil || req.Size != "" {
		t.Fatalf("no --size sent %q %v, want nothing", req.Size, err)
	}
}
