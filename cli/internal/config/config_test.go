package config

import (
	"os"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolvePrecedence(t *testing.T) {
	f := &File{Profiles: map[string]Profile{
		"default": {APIURL: "https://profile.example", APIKey: "e2b_aa", Domain: "profile.example"},
		"other":   {APIKey: "e2b_bb", DefaultTemplate: "desktop"},
	}}
	// Defaults: E2B_DOMAIN drives the API host, as it does for the SDK.
	s := Resolve(&File{Profiles: map[string]Profile{}}, Flags{}, env(map[string]string{"E2B_DOMAIN": "compute.anonalabs.com"}))
	if s.APIURL != "https://api.compute.anonalabs.com" || s.Domain != "compute.anonalabs.com" || s.Template != "base" {
		t.Fatalf("defaults: %+v", s)
	}
	s = Resolve(&File{Profiles: map[string]Profile{}}, Flags{}, env(nil))
	if s.APIURL != "https://api."+DefaultDomain {
		t.Fatalf("no env: %+v", s)
	}
	// Profile beats default, env beats profile, flag beats env.
	s = Resolve(f, Flags{}, env(nil))
	if s.APIURL != "https://profile.example" || s.APIKey != "e2b_aa" {
		t.Fatalf("profile: %+v", s)
	}
	s = Resolve(f, Flags{}, env(map[string]string{"E2B_API_KEY": "e2b_cc", "E2B_API_URL": "https://e2b-env"}))
	if s.APIKey != "e2b_cc" || s.APIURL != "https://e2b-env" {
		t.Fatalf("E2B env: %+v", s)
	}
	s = Resolve(f, Flags{}, env(map[string]string{"E2B_API_KEY": "e2b_cc", "ANONA_COMPUTE_API_KEY": "e2b_dd", "ANONA_COMPUTE_API_URL": "https://anona-env", "E2B_API_URL": "https://e2b-env"}))
	if s.APIKey != "e2b_dd" || s.APIURL != "https://anona-env" {
		t.Fatalf("ANONA env: %+v", s)
	}
	s = Resolve(f, Flags{APIKey: "e2b_ee", APIURL: "https://flag", Profile: "other"}, env(map[string]string{"ANONA_COMPUTE_API_KEY": "e2b_dd"}))
	if s.APIKey != "e2b_ee" || s.APIURL != "https://flag" || s.Template != "desktop" || s.Profile != "other" {
		t.Fatalf("flags: %+v", s)
	}
	s = Resolve(f, Flags{}, env(map[string]string{"ANONA_COMPUTE_PROFILE": "other", "E2B_SANDBOX_URL": "http://127.0.0.1:1"}))
	if s.APIKey != "e2b_bb" || s.SandboxURL != "http://127.0.0.1:1" {
		t.Fatalf("ANONA_COMPUTE_PROFILE: %+v", s)
	}
}

func TestSaveLoadMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "anona", "config.toml")
	f := &File{Profiles: map[string]Profile{"default": {APIKey: "e2b_01", Domain: "d.example"}}}
	if err := Save(path, f); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	dst, _ := os.Stat(filepath.Dir(path))
	if dst.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", dst.Mode().Perm())
	}
	g, err := Load(path)
	if err != nil || g.Profiles["default"] != f.Profiles["default"] {
		t.Fatalf("round trip: %+v %v", g, err)
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.toml")); err != nil {
		t.Fatal(err)
	}
}
