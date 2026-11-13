// Package config reads and writes the anona-compute CLI config file and resolves
// settings from flags, environment and profiles.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// DefaultDomain is the public Anona Compute domain. Customers set
// E2B_DOMAIN to it for the stock E2B SDK; the CLI uses the same value.
const DefaultDomain = "compute.anonalabs.com"

// DefaultProfile is used when no profile is selected.
const DefaultProfile = "default"

// Profile is one [profiles.<name>] table.
type Profile struct {
	APIURL          string `toml:"api_url,omitempty"`
	APIKey          string `toml:"api_key,omitempty"`
	Domain          string `toml:"domain,omitempty"`
	DefaultTemplate string `toml:"default_template,omitempty"`
	// WebURL is the dashboard origin `anona-compute login` signs in through
	// (default https://app.<domain>).
	WebURL string `toml:"web_url,omitempty"`

	// Set by `anona-compute login` (browser sign-in). The session secret itself is
	// in the OS keychain or the credentials file, never here.
	Auth            string `toml:"auth,omitempty"`             // "session" after a browser login
	CredentialStore string `toml:"credential_store,omitempty"` // "keyring" or "file"
	Org             string `toml:"org,omitempty"`              // organization slug the session acts in
	Email           string `toml:"email,omitempty"`            // signed-in user, for display
}

// AuthSession marks a profile signed in with `anona-compute login`.
const AuthSession = "session"

// File is the whole config file.
type File struct {
	Profiles map[string]Profile `toml:"profiles"`
}

// Keys lists the settable profile keys.
var Keys = []string{"api_url", "api_key", "domain", "default_template", "web_url"}

// Path returns $ANONA_COMPUTE_CONFIG, else $XDG_CONFIG_HOME/anona-compute/config.toml
// (the OS user config dir when XDG_CONFIG_HOME is unset). The compute
// directory keeps it apart from the Anona Memory CLI's files.
func Path() (string, error) {
	if p := os.Getenv("ANONA_COMPUTE_CONFIG"); p != "" {
		return p, nil
	}
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		var err error
		dir, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(dir, "anona-compute", "config.toml"), nil
}

// Load reads the file. A missing file gives an empty config.
func Load(path string) (*File, error) {
	f := &File{Profiles: map[string]Profile{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if f.Profiles == nil {
		f.Profiles = map[string]Profile{}
	}
	return f, nil
}

// Save writes the file with mode 0600 (directory 0700), atomically.
func Save(path string, f *File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := toml.Marshal(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.toml")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// Get returns a profile key's value.
func (p Profile) Get(key string) (string, error) {
	switch key {
	case "api_url":
		return p.APIURL, nil
	case "api_key":
		return p.APIKey, nil
	case "domain":
		return p.Domain, nil
	case "default_template":
		return p.DefaultTemplate, nil
	case "web_url":
		return p.WebURL, nil
	}
	return "", fmt.Errorf("unknown config key %q (known: %s)", key, strings.Join(Keys, ", "))
}

// Set sets a profile key.
func (p *Profile) Set(key, val string) error {
	switch key {
	case "api_url":
		p.APIURL = val
	case "api_key":
		p.APIKey = val
	case "domain":
		p.Domain = val
	case "default_template":
		p.DefaultTemplate = val
	case "web_url":
		p.WebURL = val
	default:
		return fmt.Errorf("unknown config key %q (known: %s)", key, strings.Join(Keys, ", "))
	}
	return nil
}

// ProfileNames returns the sorted profile names.
func (f *File) ProfileNames() []string {
	names := make([]string, 0, len(f.Profiles))
	for n := range f.Profiles {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Settings are the resolved values a command uses.
type Settings struct {
	Profile    string
	APIURL     string
	APIKey     string
	Domain     string
	SandboxURL string // envd URL override (E2B_SANDBOX_URL), for local testing
	Template   string
	WebURL     string // dashboard origin

	// Browser sign-in (profile auth = "session"); used when no API key is set.
	Session         bool
	CredentialStore string
	Org             string
	Email           string
}

// Flags are the global flag values; empty means unset.
type Flags struct {
	Profile string
	APIURL  string
	APIKey  string
	WebURL  string
}

// Resolve applies, in order: flags, environment, profile, defaults.
//
// Environment variables are ANONA_COMPUTE_*, so they never mix with the
// Anona Memory CLI's ANONA_* settings; E2B_* are what the E2B SDKs read.
//
// Domain: ANONA_COMPUTE_DOMAIN, ANONA_DOMAIN, E2B_DOMAIN, then the profile.
// API URL: --api-url, ANONA_COMPUTE_API_URL, E2B_API_URL, profile api_url, then
// https://api.<domain> (the SDK's own fallback).
// API key: --api-key, ANONA_COMPUTE_API_KEY, ANONA_API_KEY only when it holds
// an Anona Compute key (e2b_..., as the Anona SDKs accept; other values belong
// to another Anona product), E2B_API_KEY, profile api_key.
// Dashboard: --web-url, ANONA_COMPUTE_WEB_URL, profile web_url, https://app.<domain>.
func Resolve(f *File, fl Flags, getenv func(string) string) Settings {
	name := first(fl.Profile, getenv("ANONA_COMPUTE_PROFILE"), DefaultProfile)
	p := f.Profiles[name]
	s := Settings{Profile: name}
	s.Domain = first(getenv("ANONA_COMPUTE_DOMAIN"), getenv("ANONA_DOMAIN"), getenv("E2B_DOMAIN"), p.Domain, DefaultDomain)
	s.APIURL = first(fl.APIURL, getenv("ANONA_COMPUTE_API_URL"), getenv("E2B_API_URL"), p.APIURL, "https://api."+s.Domain)
	s.APIKey = first(fl.APIKey, getenv("ANONA_COMPUTE_API_KEY"), computeKey(getenv("ANONA_API_KEY")), getenv("E2B_API_KEY"), p.APIKey)
	s.SandboxURL = first(getenv("ANONA_COMPUTE_SANDBOX_URL"), getenv("E2B_SANDBOX_URL"))
	s.Template = first(p.DefaultTemplate, "base")
	s.WebURL = strings.TrimRight(first(fl.WebURL, getenv("ANONA_COMPUTE_WEB_URL"), p.WebURL, "https://app."+s.Domain), "/")
	s.Session = p.Auth == AuthSession
	s.CredentialStore, s.Org, s.Email = p.CredentialStore, p.Org, p.Email
	return s
}

func computeKey(k string) string {
	if strings.HasPrefix(k, "e2b_") || strings.HasPrefix(k, "ac_") {
		return k
	}
	return ""
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
