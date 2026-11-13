package fakee2b

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

const specSHA256 = "99fccbc60e6d0cae998703e1f5f7ad727a3cf86b013fe37d9545e8610673ff98"

type specOp struct {
	statuses []int
}

// loadSpec reads paths, methods and response codes from the pinned
// openapi.yml with a line scanner (the file's indentation is regular:
// paths at 2 spaces, methods at 4, "responses:" at 6, codes at 8).
func loadSpec(t *testing.T) (map[string]*specOp, []byte) {
	t.Helper()
	data, err := os.ReadFile("testdata/openapi.yml")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != specSHA256 {
		t.Fatalf("testdata/openapi.yml changed: sha256 %x", sum)
	}
	ops := map[string]*specOp{}
	pathRE := regexp.MustCompile(`^  (/[^:]*):\s*$`)
	methodRE := regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
	codeRE := regexp.MustCompile(`^        "(\d{3})":`)
	var curPath string
	var cur *specOp
	inResp := false
	inPaths := false
	sc := bufio.NewScanner(strings.NewReader(string(data)))
	for sc.Scan() {
		line := sc.Text()
		if line == "paths:" {
			inPaths = true
			continue
		}
		if inPaths && len(line) > 0 && line[0] != ' ' && line[0] != '#' {
			inPaths = false
		}
		if !inPaths {
			continue
		}
		if m := pathRE.FindStringSubmatch(line); m != nil {
			curPath, cur, inResp = m[1], nil, false
			continue
		}
		if m := methodRE.FindStringSubmatch(line); m != nil {
			cur = &specOp{}
			ops[strings.ToUpper(m[1])+" "+curPath] = cur
			inResp = false
			continue
		}
		if cur == nil {
			continue
		}
		if line == "      responses:" {
			inResp = true
			continue
		}
		if strings.HasPrefix(line, "      ") && !strings.HasPrefix(line, "       ") {
			inResp = false
		}
		if inResp {
			if m := codeRE.FindStringSubmatch(line); m != nil {
				n, _ := strconv.Atoi(m[1])
				cur.statuses = append(cur.statuses, n)
			}
		}
	}
	return ops, data
}

// requiredOf returns components.schemas.<name>.required from the spec.
func requiredOf(t *testing.T, spec []byte, name string) []string {
	t.Helper()
	lines := strings.Split(string(spec), "\n")
	for i, l := range lines {
		if l != "    "+name+":" {
			continue
		}
		var req []string
		in := false
		for _, l2 := range lines[i+1:] {
			if strings.HasPrefix(l2, "    ") && !strings.HasPrefix(l2, "     ") {
				break
			}
			if l2 == "      required:" {
				in = true
				continue
			}
			if in {
				if strings.HasPrefix(l2, "        - ") {
					req = append(req, strings.TrimPrefix(l2, "        - "))
					continue
				}
				in = false
			}
		}
		if len(req) == 0 {
			t.Fatalf("schema %s has no required list", name)
		}
		return req
	}
	t.Fatalf("schema %s not in spec", name)
	return nil
}

// Every route the fake serves is in the pinned spec with that method, and
// every status the fake can return is a documented response.
func TestRoutesMatchSpec(t *testing.T) {
	ops, _ := loadSpec(t)
	if len(ops) < 70 {
		t.Fatalf("parsed only %d operations", len(ops))
	}
	for _, r := range Routes {
		op, ok := ops[r.Method+" "+r.Path]
		if !ok {
			t.Errorf("%s %s is not in the spec", r.Method, r.Path)
			continue
		}
		for _, st := range r.Statuses {
			if !slices.Contains(op.statuses, st) {
				t.Errorf("%s %s: status %d not in spec responses %v", r.Method, r.Path, st, op.statuses)
			}
		}
	}
}

// Responses carry every field the spec marks required.
func TestResponsesHaveRequiredFields(t *testing.T) {
	_, spec := loadSpec(t)
	const key = "e2b_00ff"
	s := New(key)
	hs := httptest.NewServer(s.Handler())
	defer hs.Close()
	call := func(method, path, body string, want int) []byte {
		req, _ := http.NewRequest(method, hs.URL+path, strings.NewReader(body))
		req.Header.Set("X-API-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d, want %d", method, path, resp.StatusCode, want)
		}
		var b []byte
		buf := make([]byte, 1<<16)
		for {
			n, err := resp.Body.Read(buf)
			b = append(b, buf[:n]...)
			if err != nil {
				break
			}
		}
		return b
	}
	check := func(schema string, raw []byte) {
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatalf("%s: %v: %s", schema, err, raw)
		}
		for _, f := range requiredOf(t, spec, schema) {
			if _, ok := m[f]; !ok {
				t.Errorf("%s response lacks required field %q: %s", schema, f, raw)
			}
		}
	}
	created := call("POST", "/v2/sandboxes", `{"templateID":"base"}`, 201)
	check("Sandbox", created)
	var sb map[string]any
	_ = json.Unmarshal(created, &sb)
	id := sb["sandboxID"].(string)
	check("SandboxDetail", call("GET", "/sandboxes/"+id, "", 200))
	var listed []json.RawMessage
	_ = json.Unmarshal(call("GET", "/v2/sandboxes", "", 200), &listed)
	check("ListedSandbox", listed[0])
	check("Sandbox", call("POST", "/v2/sandboxes/"+id+"/connect", "", 200))
	var forks []map[string]json.RawMessage
	_ = json.Unmarshal(call("POST", "/sandboxes/"+id+"/fork", `{"count":1}`, 201), &forks)
	check("Sandbox", forks[0]["sandbox"])
	s.AddLog(id, LogEntry{Timestamp: time.Now(), Level: "info", Message: "x"})
	logs := call("GET", "/v2/sandboxes/"+id+"/logs", "", 200)
	check("SandboxLogsV2Response", logs)
	var lr map[string][]json.RawMessage
	_ = json.Unmarshal(logs, &lr)
	check("SandboxLogEntry", lr["logs"][0])
	now := time.Now()
	s.AddMetric(id, Metric{Timestamp: now, TimestampUnix: now.Unix(), CPUCount: 2, CPUUsedPct: 1.5,
		MemUsed: 1 << 20, MemTotal: 1 << 30, MemCache: 1 << 19, DiskUsed: 1 << 21, DiskTotal: 1 << 31})
	var metrics []json.RawMessage
	_ = json.Unmarshal(call("GET", "/sandboxes/"+id+"/metrics", "", 200), &metrics)
	check("SandboxMetric", metrics[0])
	check("Error", call("GET", "/sandboxes/nope", "", 404))
	check("Error", call("POST", "/v2/sandboxes", `{}`, 400))
}
