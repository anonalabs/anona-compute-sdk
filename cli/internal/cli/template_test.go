package cli

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
)

func TestTarContextDeterministicAndIgnore(t *testing.T) {
	dir := t.TempDir()
	write := func(p, c string) {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, p), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Dockerfile", "FROM x\n")
	write("app/main.py", "print(1)\n")
	write("node_modules/big/index.js", "x")
	write("secret.env", "KEY=1")
	write(".dockerignore", "# comment\nnode_modules\n*.env\n")
	p1, h1, _, err := tarContext(dir, "Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(p1)
	// Touching files does not change the hash; changing content does.
	later := time.Now().Add(time.Hour)
	_ = os.Chtimes(filepath.Join(dir, "app/main.py"), later, later)
	p2, h2, _, _ := tarContext(dir, "Dockerfile")
	defer os.Remove(p2)
	if h1 != h2 {
		t.Errorf("hash changed with mtime: %s %s", h1, h2)
	}
	write("app/main.py", "print(2)\n")
	p3, h3, _, _ := tarContext(dir, "Dockerfile")
	defer os.Remove(p3)
	if h3 == h1 {
		t.Error("hash did not change with content")
	}
	f, _ := os.Open(p1)
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var names []string
	for {
		h, err := tr.Next()
		if err != nil {
			break
		}
		names = append(names, h.Name)
	}
	sort.Strings(names)
	want := []string{".dockerignore", "Dockerfile", "app/", "app/main.py"}
	if len(names) != len(want) {
		t.Fatalf("entries %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("entries %v, want %v", names, want)
		}
	}
}

// A template whose newest build failed after an earlier one succeeded is still
// ready; list says so and adds a note, rather than showing it as broken.
func TestTemplateStatusNotesAFailedRebuild(t *testing.T) {
	for _, c := range []struct {
		t    api.Template
		want string
	}{
		{api.Template{BuildStatus: "ready", Phase: "ready"}, "ready"},
		{api.Template{BuildStatus: "ready", Phase: "ready", LastBuildStatus: "error"}, "ready (last build failed)"},
		{api.Template{BuildStatus: "building", Phase: "registering"}, "building (registering)"},
		{api.Template{BuildStatus: "error", Phase: "failed"}, "error (failed)"},
	} {
		if got := templateStatus(&c.t); got != c.want {
			t.Errorf("templateStatus(%+v) = %q, want %q", c.t, got, c.want)
		}
	}
}
