package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildNetwork is the only place the CLI writes a vault reference, and a
// malformed one is a 403 from the policy compiler rather than anything the
// person can read here. So the shape it produces is pinned.
func TestBuildNetworkVaultInject(t *testing.T) {
	raw, err := buildNetwork("", []string{"api.github.com"},
		[]string{"api.github.com:Authorization=bearer:gh_token", "api.github.com:X-Trace=plain_secret"})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		AllowOut []string `json:"allowOut"`
		Rules    map[string][]struct {
			Transform struct {
				Headers map[string]string `json:"headers"`
			} `json:"transform"`
		} `json:"rules"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("not valid JSON: %v (%s)", err, raw)
	}
	if len(got.AllowOut) != 1 || got.AllowOut[0] != "api.github.com" {
		t.Errorf("allowOut = %v", got.AllowOut)
	}
	// Both headers for one host belong to one rule entry: two entries would be
	// two bindings where one is meant.
	rules := got.Rules["api.github.com"]
	if len(rules) != 1 {
		t.Fatalf("want 1 rule for the host, got %d", len(rules))
	}
	h := rules[0].Transform.Headers
	if h["Authorization"] != "Bearer ${vault.gh_token}" {
		t.Errorf("Authorization = %q", h["Authorization"])
	}
	if h["X-Trace"] != "${vault.plain_secret}" {
		t.Errorf("X-Trace = %q", h["X-Trace"])
	}
}

func TestBuildNetworkBasicFormat(t *testing.T) {
	raw, err := buildNetwork("", nil, []string{"registry.example.com:Authorization=basic:reg_creds"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"Basic ${vault.reg_creds}"`) {
		t.Errorf("basic: format not applied: %s", raw)
	}
}

// No network flags must leave the create request exactly as it was before the
// flags existed, so an unrelated create does not start sending a network
// document the gateway then has to validate.
func TestBuildNetworkEmptyIsNil(t *testing.T) {
	raw, err := buildNetwork("", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw != nil {
		t.Errorf("want nil, got %s", raw)
	}
}

func TestBuildNetworkRejectsBadInject(t *testing.T) {
	for _, bad := range []string{
		"api.github.com",                             // no header
		"api.github.com:Authorization",               // no secret
		"api.github.com:Authorization=Bearer tok",    // a literal value, not a reference
		"api.github.com:Authorization=bogus:gh",      // unknown format prefix
		"api.github.com:Authorization=bearer:GH_TOK", // uppercase is not a vault name
	} {
		if _, err := buildNetwork("", nil, []string{bad}); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func TestBuildNetworkRejectsDuplicateHeader(t *testing.T) {
	_, err := buildNetwork("", nil, []string{
		"api.github.com:Authorization=bearer:a",
		"api.github.com:Authorization=bearer:b",
	})
	if err == nil {
		t.Fatal("a header given twice for one host was accepted")
	}
}

// --network-file replaces the document, so combining it with the convenience
// flags would silently drop them.
func TestBuildNetworkFileRefusesCombination(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "net.json")
	if err := os.WriteFile(f, []byte(`{"allowOut":["example.com"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildNetwork(f, []string{"other.com"}, nil); err == nil {
		t.Error("--network-file with --allow-out was accepted")
	}
	raw, err := buildNetwork(f, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"allowOut":["example.com"]}` {
		t.Errorf("document was not passed through unchanged: %s", raw)
	}
}

func TestBuildNetworkFileRejectsNonJSON(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "net.json")
	if err := os.WriteFile(f, []byte("allowOut: [example.com]"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := buildNetwork(f, nil, nil); err == nil {
		t.Error("a non-JSON network file was accepted")
	}
}

// A secret that picks up the trailing newline of the file it was written in
// becomes an Authorization header with a newline in it. One is stripped; a
// value that legitimately ends in whitespace keeps the rest.
func TestReadSecretValueStripsOneNewline(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ in, want string }{
		{"tok", "tok"},
		{"tok\n", "tok"},
		{"tok\r\n", "tok"},
		{"tok\n\n", "tok\n"},
		{"tok ", "tok "},
	} {
		f := filepath.Join(dir, "v")
		if err := os.WriteFile(f, []byte(tc.in), 0o600); err != nil {
			t.Fatal(err)
		}
		a := &app{}
		got, err := a.readSecretValue(f)
		if err != nil {
			t.Fatalf("%q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("%q -> %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReadSecretValueFromStdin(t *testing.T) {
	a := &app{stdin: bytes.NewBufferString("piped-token\n")}
	got, err := a.readSecretValue("")
	if err != nil {
		t.Fatal(err)
	}
	if got != "piped-token" {
		t.Errorf("got %q", got)
	}
}

func TestReadSecretValueRejectsEmpty(t *testing.T) {
	a := &app{stdin: bytes.NewBufferString("\n")}
	if _, err := a.readSecretValue(""); err == nil {
		t.Error("an empty value was accepted")
	}
}

// vault list --json is one object whose secrets are an array, so an empty
// vault is "secrets":[] and never null or nothing at all.
func TestVaultListJSONEmptyIsAnArray(t *testing.T) {
	b, err := snakeJSON(map[string]any{"enabled": true, "secrets": vaultSecretViews(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(b), `{"enabled":true,"secrets":[]}`+"\n"; got != want {
		t.Fatalf("vault list --json, empty: %q, want %q", got, want)
	}
}
