package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
)

// Credential stores. "keyring" is the OS keychain (macOS Keychain, Windows
// Credential Manager, the Secret Service on Linux); "file" is a 0600 JSON
// file next to config.toml.
const (
	StoreKeyring = "keyring"
	StoreFile    = "file"
)

// KeyringService is the keychain service name.
const KeyringService = "anona-compute"

// ErrNoCredential means nothing is stored for the profile.
var ErrNoCredential = errors.New("no stored CLI session for this profile; run `anona-compute login`")

// Store saves the session secret of each profile.
type Store struct {
	// FilePath is the credentials file (config dir/credentials.json).
	FilePath string
	// Mode: "keyring", "file", or "" / "auto" (keyring, else file).
	Mode string
}

func account(profile string) string { return "profile:" + profile }

// Save stores secret and returns the store used.
func (s *Store) Save(profile, secret string) (string, error) {
	switch s.Mode {
	case StoreFile:
		return StoreFile, s.fileSet(profile, secret)
	case StoreKeyring:
		if err := keyring.Set(KeyringService, account(profile), secret); err != nil {
			return "", fmt.Errorf("OS keychain: %w", err)
		}
		return StoreKeyring, nil
	case "", "auto":
		if err := keyring.Set(KeyringService, account(profile), secret); err == nil {
			// Drop a stale file copy from an earlier fallback.
			_ = s.fileDelete(profile)
			return StoreKeyring, nil
		}
		return StoreFile, s.fileSet(profile, secret)
	}
	return "", fmt.Errorf("unknown credential store %q (keyring, file or auto)", s.Mode)
}

// Load reads the secret from store (as recorded in the profile).
func (s *Store) Load(store, profile string) (string, error) {
	if store == StoreKeyring {
		v, err := keyring.Get(KeyringService, account(profile))
		if errors.Is(err, keyring.ErrNotFound) {
			return "", ErrNoCredential
		}
		if err != nil {
			return "", fmt.Errorf("OS keychain: %w", err)
		}
		return v, nil
	}
	m, err := s.fileRead()
	if err != nil {
		return "", err
	}
	v, ok := m[profile]
	if !ok || v == "" {
		return "", ErrNoCredential
	}
	return v, nil
}

// Delete removes the secret from store. A missing entry is not an error.
func (s *Store) Delete(store, profile string) error {
	if store == StoreKeyring {
		err := keyring.Delete(KeyringService, account(profile))
		if err != nil && !errors.Is(err, keyring.ErrNotFound) {
			return fmt.Errorf("OS keychain: %w", err)
		}
		return nil
	}
	return s.fileDelete(profile)
}

func (s *Store) fileRead() (map[string]string, error) {
	m := map[string]string{}
	data, err := os.ReadFile(s.FilePath)
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.FilePath, err)
	}
	return m, nil
}

func (s *Store) fileWrite(m map[string]string) error {
	dir := filepath.Dir(s.FilePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if len(m) == 0 {
		err := os.Remove(s.FilePath)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), s.FilePath); err != nil {
		return err
	}
	return os.Chmod(s.FilePath, 0o600)
}

func (s *Store) fileSet(profile, secret string) error {
	m, err := s.fileRead()
	if err != nil {
		return err
	}
	m[profile] = secret
	return s.fileWrite(m)
}

func (s *Store) fileDelete(profile string) error {
	m, err := s.fileRead()
	if err != nil {
		return err
	}
	if _, ok := m[profile]; !ok {
		return nil
	}
	delete(m, profile)
	return s.fileWrite(m)
}
