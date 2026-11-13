package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/fakee2b"
)

// Sizes and paths the gateway would refuse are refused before any request.
func TestTemplateBuildValidatesFirst(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM ubuntu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := t.TempDir()
	cases := []struct {
		args []string
		want string
	}{
		{[]string{dir, "--cpu", "0"}, "--cpu must be 1 to 8"},
		{[]string{dir, "--cpu", "9"}, "--cpu must be 1 to 8"},
		{[]string{dir, "--memory", "99999"}, "--memory must be 128 to 16384 MiB"},
		{[]string{dir, "--memory", "64"}, "--memory must be 128 to 16384 MiB"},
		{[]string{dir, "--disk", "512"}, "--disk must be 1024 to 81920 MiB, not 512"},
		{[]string{dir, "--disk", "81921"}, "--disk must be 1024 to 81920 MiB, not 81921"},
		{[]string{"/missing/dir"}, "context directory not found: /missing/dir"},
		{[]string{empty}, "no Dockerfile in " + empty},
		{[]string{dir, "-f", filepath.Join(dir, "Nope")}, "Dockerfile not found: " + filepath.Join(dir, "Nope")},
		{[]string{filepath.Join(dir, "Dockerfile")}, "is not a directory"},
		{[]string{dir, empty}, "one context directory, not 2 arguments"},
	}
	for _, c := range cases {
		r := e.run(append([]string{"template", "build"}, c.args...)...)
		if r.code != 2 || !strings.Contains(r.stderr, c.want) {
			t.Errorf("%v: exit %d stderr %q, want %q", c.args, r.code, r.stderr, c.want)
		}
	}
	if reqs := e.srv.Requests(); len(reqs) != 0 {
		t.Fatalf("validation made %d requests, first %s %s", len(reqs), reqs[0].Method, reqs[0].Path)
	}

	// --wait=false: labelled lines.
	r := e.must("template", "build", dir, "--name", "mine", "--cpu", "2", "--memory", "1024", "--wait=false")
	if r.stdout != "Template ID: tpl-mine\nBuild ID: bld-1\n" {
		t.Fatalf("--wait=false stdout %q", r.stdout)
	}
}

// --disk sizes the template's writable disk. It is sent as the gateway's
// diskSizeMB and as the E2B minFreeDiskMb that comes to the same disk, so an
// older gateway honours it too; the accepted size is shown.
func TestTemplateBuildDisk(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM ubuntu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := e.must("template", "build", dir, "--name", "big", "--cpu", "4", "--memory", "8192", "--disk", "20480", "--wait=false")
	if !strings.Contains(r.stderr, "Size: 4 vCPU, 8192 MiB memory, 20480 MiB disk") {
		t.Fatalf("stderr does not show the size: %q", r.stderr)
	}
	var body map[string]any
	for _, req := range e.srv.Requests() {
		if req.Method == "POST" && req.Path == "/v3/templates" {
			if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if body["diskSizeMB"] != float64(20480) || body["minFreeDiskMb"] != float64(20480-512) ||
		body["cpuCount"] != float64(4) || body["memoryMB"] != float64(8192) {
		t.Fatalf("POST /v3/templates body %v", body)
	}

	// No --disk: neither field is sent, and the gateway's default is shown.
	r = e.must("template", "build", dir, "--name", "small", "--wait=false")
	if !strings.Contains(r.stderr, "Size: 2 vCPU, 2048 MiB memory, 1024 MiB disk") {
		t.Fatalf("default size not shown: %q", r.stderr)
	}
	reqs := e.srv.Requests()
	last := reqs[len(reqs)-1]
	for _, req := range reqs {
		if req.Method == "POST" && req.Path == "/v3/templates" {
			last = req
		}
	}
	if strings.Contains(last.Body, "diskSizeMB") || strings.Contains(last.Body, "minFreeDiskMb") {
		t.Fatalf("a build without --disk sent a disk: %s", last.Body)
	}

	// --json carries the size.
	row := jsonLines(t, e.must("template", "build", dir, "--name", "big", "--disk", "4096", "--wait=false", "--json").stdout)[0]
	if row["disk_size_mb"] != float64(4096) || row["template_id"] != "tpl-big" {
		t.Fatalf("--json row %v", row)
	}
}

// `template list` shows each template's kept image storage in SIZE and the
// team's usage against its plan under the table; a gateway without the usage
// route just leaves the footer out.
func TestTemplateListSizeAndUsage(t *testing.T) {
	e := newEnv(t)
	e.srv.Configure(func(s *fakee2b.Server) {
		s.Templates = []map[string]any{
			{"templateID": "t-big", "names": []string{"big"}, "buildStatus": "ready", "cpuCount": 2, "memoryMB": 1024, "diskSizeMB": 1024,
				"imageBytes": 2254857830, "storageBytes": 4509715660},
			{"templateID": "t-new", "names": []string{"new"}, "buildStatus": "building", "cpuCount": 2, "memoryMB": 1024, "diskSizeMB": 1024},
		}
		s.TemplateUsage = map[string]any{"templates": 3, "maxTemplates": 10, "storageBytes": 4509715660,
			"maxStorageBytes": 53687091200, "maxImageBytes": 10737418240}
	})
	r := e.must("template", "list")
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if !strings.Contains(lines[0], "SIZE") {
		t.Fatalf("header lacks SIZE:\n%s", r.stdout)
	}
	if !strings.Contains(lines[1], "4.2 GiB") || !strings.Contains(lines[2], " - ") {
		t.Errorf("SIZE cells:\n%s", r.stdout)
	}
	if last := lines[len(lines)-1]; last != "3 of 10 templates · 4.2 of 50 GiB" {
		t.Errorf("footer %q", last)
	}
	rows := jsonList(t, e.must("template", "list", "--json").stdout)
	if rows[0]["storage_bytes"] != float64(4509715660) || rows[0]["image_bytes"] != float64(2254857830) || rows[1]["storage_bytes"] != nil {
		t.Errorf("--json rows %v", rows)
	}

	// No storage limit on the plan: usage alone.
	e.srv.Configure(func(s *fakee2b.Server) { s.TemplateUsage["maxStorageBytes"] = nil })
	if r := e.must("template", "list"); !strings.HasSuffix(r.stdout, "3 of 10 templates · 4.2 GiB\n") {
		t.Errorf("unlimited storage footer:\n%s", r.stdout)
	}
	// An older gateway: no footer, no error.
	e.srv.Configure(func(s *fakee2b.Server) { s.TemplateUsage = nil })
	if r := e.must("template", "list"); strings.Contains(r.stdout, "templates ·") || r.stderr != "" {
		t.Errorf("without the usage route: stdout %q stderr %q", r.stdout, r.stderr)
	}
}

func TestTemplateListAll(t *testing.T) {
	e := newEnv(t)
	// A fresh account: no custom templates, but the hint names --all.
	r := e.must("template", "list")
	if !strings.Contains(r.stderr, "template list --all") {
		t.Fatalf("empty list hint: %q", r.stderr)
	}
	e.srv.Configure(func(s *fakee2b.Server) {
		s.Templates = []map[string]any{
			{"templateID": "t-ready", "names": []string{"mine"}, "buildStatus": "ready", "cpuCount": 2, "memoryMB": 1024},
			{"templateID": "t-building", "names": []string{"wip"}, "buildStatus": "building", "phase": "pushing", "cpuCount": 4, "memoryMB": 4096},
		}
	})
	r = e.must("template", "list", "--all")
	for _, want := range []string{"SOURCE", "STATUS", "base", "agent-desktop", "built-in", "custom", "mine", "wip", "building (pushing)"} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("--all output lacks %q:\n%s", want, r.stdout)
		}
	}
	if strings.Count(r.stdout, "mine") != 1 {
		t.Errorf("custom template listed twice:\n%s", r.stdout)
	}
	rows := jsonList(t, e.must("template", "list", "--all", "--json").stdout)
	sources := map[string]string{}
	for _, row := range rows {
		sources[row["name"].(string)] = row["source"].(string) + "/" + row["status"].(string)
	}
	if sources["base"] != "built-in/ready" || sources["mine"] != "custom/ready" || sources["wip"] != "custom/building (pushing)" || len(rows) != 9 {
		t.Fatalf("rows %v", sources)
	}

	// Engine asleep: listed, but marked unverified.
	e.srv.Configure(func(s *fakee2b.Server) { s.CatalogEngine = "paused" })
	r = e.must("template", "list", "--all")
	if !strings.Contains(r.stdout, "unverified") || !strings.Contains(r.stderr, "not yet confirmed") {
		t.Fatalf("asleep: stdout %q stderr %q", r.stdout, r.stderr)
	}
	// An older gateway without the catalog: custom templates, and a note.
	e.srv.Configure(func(s *fakee2b.Server) { s.NoCatalog = true })
	r = e.must("template", "list", "--all")
	if !strings.Contains(r.stdout, "mine") || strings.Contains(r.stdout, "built-in") || !strings.Contains(r.stderr, "no template catalog") {
		t.Fatalf("no catalog: stdout %q stderr %q", r.stdout, r.stderr)
	}
}

// template info used to print a built-in template at GET /templates/{id}'s
// numbers, which for every platform template were a new build's defaults
// (2 vCPU / 2048 MiB): base and desktop both said 2048 MiB while template
// list --all, and the sandboxes they start, said 1024 and 4096. It also said
// "the CPU, memory and disk above are minimums" under a minimum of small (1
// vCPU) for a template it had just printed at 2 vCPU. A built-in now takes
// its size from the catalog, as list --all does, and says its minimum is set
// for it; a custom template is unchanged.
func TestTemplateInfoBuiltInSizeComesFromTheCatalog(t *testing.T) {
	e := newEnv(t)
	small := map[string]any{"name": "small", "cpuCount": 1, "memoryMB": 2048, "diskSizeMB": 10240}
	medium := map[string]any{"name": "medium", "cpuCount": 2, "memoryMB": 4096, "diskSizeMB": 20480}
	e.srv.Configure(func(s *fakee2b.Server) {
		s.CatalogFields = map[string]map[string]any{
			"base": {"cpuCount": 2, "memoryMB": 1024, "diskSizeMB": 1024, "minimumSize": "small",
				"sizes": []any{small, medium}, "defaultSize": "small"},
			"desktop": {"cpuCount": 2, "memoryMB": 4096, "diskSizeMB": 4096, "minimumSize": "medium",
				"sizes": []any{medium}, "defaultSize": "medium"},
		}
		s.Templates = []map[string]any{{"templateID": "t-mine", "names": []string{"mine"}, "buildStatus": "ready",
			"cpuCount": 2, "memoryMB": 2048, "diskSizeMB": 1024, "minimumSize": "medium"}}
	})

	list := map[string]map[string]any{}
	for _, row := range jsonList(t, e.must("template", "list", "--all", "--json").stdout) {
		list[row["name"].(string)] = row
	}
	for _, c := range []struct {
		name, mem, disk, minimum string
	}{
		{"base", "1024 MiB", "1024 MiB", "small (1 vCPU, 2 GiB, 10 GB disk), set for this built-in template; without --size it runs at the CPU, memory and disk above"},
		{"desktop", "4096 MiB", "4096 MiB", "medium (2 vCPU, 4 GiB, 20 GB disk), set for this built-in template; without --size it runs at the CPU, memory and disk above"},
	} {
		out := e.must("template", "info", c.name).stdout
		for _, want := range []string{"CPU           2\n", "Memory        " + c.mem + "\n", "Disk          " + c.disk + "\n", "Minimum size  " + c.minimum + "\n"} {
			if !strings.Contains(out, want) {
				t.Errorf("template info %s lacks %q:\n%s", c.name, want, out)
			}
		}
		if strings.Contains(out, "2048 MiB") || strings.Contains(out, "above are minimums") {
			t.Errorf("template info %s still prints the build defaults or the custom-template wording:\n%s", c.name, out)
		}
		got := jsonLines(t, e.must("template", "info", c.name, "--json").stdout)[0]["template"].(map[string]any)
		if got["cpu_count"] != list[c.name]["cpu_count"] || got["memory_mb"] != list[c.name]["memory_mb"] {
			t.Errorf("template info %s --json %v disagrees with template list --all %v", c.name, got, list[c.name])
		}
	}

	// A custom template: its own build's numbers are its minimums, as before.
	out := e.must("template", "info", "mine").stdout
	if !strings.Contains(out, "2048 MiB\n") || strings.Contains(out, "set for this built-in") {
		t.Errorf("custom template info:\n%s", out)
	}
}
