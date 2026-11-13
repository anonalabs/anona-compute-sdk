package envd

import "testing"

func TestURL(t *testing.T) {
	// connectionConfig.ts getHost: `${port}-${sandboxId}.${sandboxDomain}` over https.
	if got := URL("abc123", "sbx.compute.anonalabs.com"); got != "https://49983-abc123.sbx.compute.anonalabs.com" {
		t.Fatal(got)
	}
}

func TestAuthHeader(t *testing.T) {
	cases := []struct{ version, user, want string }{
		{"0.4.0", "", ""},
		{"0.5.11", "", ""},
		{"0.3.9", "", "Basic dXNlcjo="}, // "user:"
		{"0.5.11", "root", "Basic cm9vdDo="},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := AuthHeader(c.version, c.user); got != c.want {
			t.Errorf("AuthHeader(%q, %q) = %q, want %q", c.version, c.user, got, c.want)
		}
	}
}
