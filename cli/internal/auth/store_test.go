package auth

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/zalando/go-keyring"
)

// TestRealKeyring uses the machine's keychain (Secret Service, Keychain,
// Credential Manager). Opt in with ANONA_KEYRING_TEST=1. It comes first:
// TestKeyringStore replaces the keychain with a mock for the rest of the run.
func TestRealKeyring(t *testing.T) {
	if os.Getenv("ANONA_KEYRING_TEST") != "1" {
		t.Skip("ANONA_KEYRING_TEST=1 not set")
	}
	s := &Store{Mode: StoreKeyring}
	if _, err := s.Save("anona-test-profile", "real-secret"); err != nil {
		t.Fatal(err)
	}
	defer s.Delete(StoreKeyring, "anona-test-profile")
	if v, err := s.Load(StoreKeyring, "anona-test-profile"); err != nil || v != "real-secret" {
		t.Fatalf("load: %q %v", v, err)
	}
}

func TestFileStore(t *testing.T) {
	s := &Store{FilePath: filepath.Join(t.TempDir(), "anona", "credentials.json"), Mode: StoreFile}
	used, err := s.Save("default", "secret-a")
	if err != nil || used != StoreFile {
		t.Fatalf("save: %v %s", err, used)
	}
	if _, err := s.Save("work", "secret-b"); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(s.FilePath)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	if d, _ := os.Stat(filepath.Dir(s.FilePath)); d.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v", d.Mode().Perm())
	}
	if v, err := s.Load(StoreFile, "work"); err != nil || v != "secret-b" {
		t.Fatalf("load: %q %v", v, err)
	}
	if err := s.Delete(StoreFile, "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(StoreFile, "work"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.Delete(StoreFile, "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.FilePath); !os.IsNotExist(err) {
		t.Fatalf("empty credentials file not removed: %v", err)
	}
}

func TestKeyringStore(t *testing.T) {
	keyring.MockInit()
	s := &Store{FilePath: filepath.Join(t.TempDir(), "credentials.json")}
	used, err := s.Save("default", "secret-k")
	if err != nil || used != StoreKeyring {
		t.Fatalf("auto save: %v %s", err, used)
	}
	if v, err := s.Load(StoreKeyring, "default"); err != nil || v != "secret-k" {
		t.Fatalf("load: %q %v", v, err)
	}
	if _, err := os.Stat(s.FilePath); !os.IsNotExist(err) {
		t.Fatal("auto mode wrote the file although the keychain worked")
	}
	if err := s.Delete(StoreKeyring, "default"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(StoreKeyring, "default"); !errors.Is(err, ErrNoCredential) {
		t.Fatalf("after delete: %v", err)
	}
	// A keychain that fails: auto falls back to the file.
	keyring.MockInitWithError(errors.New("no secret service"))
	used, err = s.Save("default", "secret-f")
	if err != nil || used != StoreFile {
		t.Fatalf("fallback: %v %s", err, used)
	}
	if _, err := (&Store{FilePath: s.FilePath, Mode: StoreKeyring}).Save("x", "y"); err == nil {
		t.Fatal("forced keyring did not fail")
	}
}
