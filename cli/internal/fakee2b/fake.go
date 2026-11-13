// Package fakee2b is an in-process stand-in for the Anona gateway, used by
// the CLI tests. It serves:
//
//   - the E2B sandbox REST routes the CLI calls, with the request and
//     response shapes of spec/openapi.yml at
//     e2b-dev/E2B@ee355b765ac0e884fbeaa69e4b44f4c7c58ed6b1 (copied byte for
//     byte to testdata/openapi.yml, sha256 99fccbc6…ff98);
//   - envd's process.Process Start, SendSignal, SendInput, CloseStdin and
//     List over the Connect protocol,
//     using connectrpc.com/connect with the descriptor of
//     spec/envd/process/process.proto from the same commit
//     (testdata/process.proto, compiled with protoc 28.3 to
//     testdata/process.binpb). Commands really run on the test machine with
//     bash, so exit codes, output and signals are real.
package fakee2b

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"
)

//go:embed testdata/process.binpb
var processDescriptorSet []byte

// EnvdVersion is the envd version the fake reports.
const EnvdVersion = "0.4.2"

// Route is one REST route the fake serves and the statuses it can return.
type Route struct {
	Method   string
	Path     string // spec path, e.g. /sandboxes/{sandboxID}
	Statuses []int
}

// Routes lists every REST route served, for the spec conformance test.
var Routes = []Route{
	{"POST", "/v2/sandboxes", []int{201, 400, 401}},
	{"GET", "/v2/sandboxes", []int{200, 400, 401}},
	{"GET", "/sandboxes/{sandboxID}", []int{200, 401, 404}},
	{"DELETE", "/sandboxes/{sandboxID}", []int{204, 401, 404}},
	{"POST", "/sandboxes/{sandboxID}/pause", []int{204, 401, 404, 409}},
	{"POST", "/v2/sandboxes/{sandboxID}/connect", []int{200, 201, 400, 401, 404}},
	{"POST", "/sandboxes/{sandboxID}/timeout", []int{204, 401, 404}},
	{"POST", "/sandboxes/{sandboxID}/fork", []int{201, 401, 404, 409}},
	{"GET", "/v2/sandboxes/{sandboxID}/logs", []int{200, 401, 404}},
	{"GET", "/sandboxes/{sandboxID}/metrics", []int{200, 401, 404}},
}

// Sandbox is the fake's record of one sandbox.
type Sandbox struct {
	ID         string
	TemplateID string
	Alias      string
	State      string
	StartedAt  time.Time
	EndAt      time.Time
	Metadata   map[string]string
	EnvVars    map[string]string
	Token      string
	PausedWith *bool // memory flag of the last pause
	AllowNet   *bool
	AutoPause  bool
	// IdleMinutes is autoPause.idleMinutes when create sent the object form
	// (nil otherwise).
	IdleMinutes *int
	CPUCount    int32
	MemoryMB    int32
	DiskSizeMB  int32
	Logs        []LogEntry
	Metrics     []Metric
	ForkedFrom  string
	LastTimeout int32
}

// LogEntry is components.schemas.SandboxLogEntry.
type LogEntry struct {
	Timestamp time.Time         `json:"timestamp"`
	Message   string            `json:"message"`
	Level     string            `json:"level"`
	Fields    map[string]string `json:"fields"`
}

// Metric is components.schemas.SandboxMetric.
type Metric struct {
	Timestamp     time.Time `json:"timestamp"`
	TimestampUnix int64     `json:"timestampUnix"`
	CPUCount      int       `json:"cpuCount"`
	CPUUsedPct    float64   `json:"cpuUsedPct"`
	MemUsed       int64     `json:"memUsed"`
	MemTotal      int64     `json:"memTotal"`
	MemCache      int64     `json:"memCache"`
	DiskUsed      int64     `json:"diskUsed"`
	DiskTotal     int64     `json:"diskTotal"`
}

// Request is a recorded HTTP request.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   string
}

// Server is the fake gateway plus envd.
type Server struct {
	APIKey string
	Domain string
	// Bearers are accepted as Authorization: Bearer <token> (dashboard
	// JWTs, as `anona-compute login` sends them), next to the API key.
	Bearers func(token string) bool
	// UsageSandboxes is how many sandboxes GET /v1/usage has in the period (default 1), paged by limit and cursor.
	UsageSandboxes int

	mu        sync.Mutex
	sandboxes map[string]*Sandbox
	order     []string
	requests  []Request
	nextID    int
	procs     map[uint32]*exec.Cmd
	procCalls map[uint32]StartCall
	stdins    map[uint32]io.WriteCloser
	starts    []StartCall
	signals   []string
	faults    []*Fault
	// Templates is what GET /v2/templates returns (E2B Template objects).
	Templates []map[string]any
	// Platform is the platform part of GET /v1/template-catalog (names).
	Platform []string
	// NoCatalog makes GET /v1/template-catalog 404, as an older gateway.
	NoCatalog bool
	// CatalogFields adds fields to a platform template's catalog entry, by
	// name (cpuCount, memoryMB, diskSizeMB, minimumSize, sizes, defaultSize).
	CatalogFields map[string]map[string]any
	// CatalogEngine, when set, is the catalog's engine state (the engine is
	// asleep and the list is not verified against it).
	CatalogEngine string
	// TemplateUsage is what GET /v1/templates/usage returns; nil answers 404,
	// as a gateway without it.
	TemplateUsage map[string]any
}

// Configure changes the exported settings (Templates, Platform, NoCatalog,
// CatalogEngine) under the server's lock. A test must use it rather than
// assign them directly once the server has answered a request: the handler
// goroutines read them under the lock, and the CLI under test runs in another
// process, so nothing else orders the write before the next read and
// `go test -race` reports it.
func (s *Server) Configure(fn func(*Server)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s)
}

// DefaultPlatform is the platform catalog the fake serves by default.
var DefaultPlatform = []string{"base", "python", "node", "code-interpreter", "desktop", "coding-agent", "agent-desktop"}

func (s *Server) catalogLocked() []map[string]any {
	out := []map[string]any{}
	for _, name := range s.Platform {
		e := map[string]any{"templateID": name, "label": name, "aliases": []string{name}, "public": true,
			"description": "platform template " + name, "cpuCount": nil, "memoryMB": nil}
		for k, v := range s.CatalogFields[name] {
			e[k] = v
		}
		out = append(out, e)
	}
	for _, t := range s.Templates {
		if t["buildStatus"] != "ready" {
			continue // the catalog lists what can be launched
		}
		label, _ := t["templateID"].(string)
		var aliases []string
		if names, ok := t["names"].([]string); ok && len(names) > 0 {
			label, aliases = names[0], names
		}
		out = append(out, map[string]any{"templateID": t["templateID"], "label": label, "aliases": aliases, "public": false,
			"cpuCount": t["cpuCount"], "memoryMB": t["memoryMB"]})
	}
	return out
}

// StartCall records an envd Start request as decoded by connect-go.
type StartCall struct {
	SandboxID   string
	AccessToken string
	Port        string
	ContentType string
	Cmd         string
	Args        []string
	Envs        map[string]string
	Cwd         string
	Auth        string
	// Stdin is the request's stdin flag (true when unset, as in envd).
	Stdin bool
}

// New returns a server that accepts the given API key.
func New(apiKey string) *Server {
	return &Server{APIKey: apiKey, Domain: "sbx.anona.test", sandboxes: map[string]*Sandbox{}, procs: map[uint32]*exec.Cmd{},
		procCalls: map[uint32]StartCall{}, stdins: map[uint32]io.WriteCloser{}, Platform: DefaultPlatform}
}

// Handler returns the HTTP handler for the REST API and envd.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	// The gateway's public liveness and version routes (internal/server).
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"version": "fake-1"})
	})
	mux.HandleFunc("POST /v2/sandboxes", s.auth(s.create))
	mux.HandleFunc("GET /v2/sandboxes", s.auth(s.list))
	mux.HandleFunc("GET /sandboxes/{sandboxID}", s.auth(s.get))
	mux.HandleFunc("DELETE /sandboxes/{sandboxID}", s.auth(s.kill))
	mux.HandleFunc("POST /sandboxes/{sandboxID}/pause", s.auth(s.pause))
	mux.HandleFunc("POST /v2/sandboxes/{sandboxID}/connect", s.auth(s.connect))
	mux.HandleFunc("POST /sandboxes/{sandboxID}/timeout", s.auth(s.timeout))
	mux.HandleFunc("POST /sandboxes/{sandboxID}/fork", s.auth(s.fork))
	mux.HandleFunc("GET /v2/sandboxes/{sandboxID}/logs", s.auth(s.logs))
	mux.HandleFunc("GET /sandboxes/{sandboxID}/metrics", s.auth(s.metrics))
	mux.HandleFunc("GET /v2/templates", s.auth(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := s.Templates
		if out == nil {
			out = []map[string]any{}
		}
		writeJSON(w, 200, out)
	}))
	// The team's template usage (gateway internal/templates/api_usage.go).
	mux.HandleFunc("GET /v1/templates/usage", s.auth(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.TemplateUsage == nil {
			writeErr(w, r, 404, "not found")
			return
		}
		writeJSON(w, 200, s.TemplateUsage)
	}))
	// The gateway's template catalog (handlers/catalog): platform templates
	// plus the caller's own, as {"templates":[...]}. Not part of the E2B spec.
	mux.HandleFunc("GET /v1/template-catalog", s.auth(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.NoCatalog {
			writeErr(w, r, 404, "not found")
			return
		}
		out := map[string]any{"templates": s.catalogLocked()}
		if s.CatalogEngine != "" {
			out["engine"] = map[string]any{"state": s.CatalogEngine, "verified": false}
		}
		writeJSON(w, 200, out)
	}))
	// Template builds, as far as `template build` needs them: the context is
	// always "already uploaded".
	mux.HandleFunc("POST /v3/templates", s.auth(func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req struct {
			Name          string `json:"name"`
			CPUCount      int    `json:"cpuCount"`
			MemoryMB      int    `json:"memoryMB"`
			DiskSizeMB    int    `json:"diskSizeMB"`
			MinFreeDiskMb int    `json:"minFreeDiskMb"`
		}
		_ = json.Unmarshal(body, &req)
		// The gateway's defaults and disk rule (internal/templates/api.go).
		cpu, mem, disk := max(req.CPUCount, 0), max(req.MemoryMB, 0), 1024
		if cpu == 0 {
			cpu = 2
		}
		if mem == 0 {
			mem = 2048
		}
		switch {
		case req.DiskSizeMB > 0:
			disk = req.DiskSizeMB
		case req.MinFreeDiskMb > 0:
			disk = max(disk, req.MinFreeDiskMb+512)
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"templateID": "tpl-" + req.Name, "buildID": "bld-1", "names": []string{req.Name}, "tags": []string{},
			"cpuCount": cpu, "memoryMB": mem, "diskSizeMB": disk})
	}))
	// One template with its builds. A platform template answers as the
	// gateway did before it knew platform sizes: a new build's defaults
	// (2 vCPU / 2048 MiB / 1024 MiB) whatever the template is, so a test can
	// tell which source the CLI printed.
	mux.HandleFunc("GET /templates/{templateID}", s.auth(func(w http.ResponseWriter, r *http.Request, _ []byte) {
		s.mu.Lock()
		defer s.mu.Unlock()
		ref := r.PathValue("templateID")
		for _, t := range s.Templates {
			names, _ := t["names"].([]string)
			if t["templateID"] == ref || slices.Contains(names, ref) {
				out := map[string]any{"builds": []any{}}
				for k, v := range t {
					out[k] = v
				}
				writeJSON(w, 200, out)
				return
			}
		}
		if slices.Contains(s.Platform, ref) {
			writeJSON(w, 200, map[string]any{"templateID": ref, "names": []string{ref}, "aliases": []string{ref}, "public": true,
				"buildStatus": "ready", "cpuCount": 2, "memoryMB": 2048, "diskSizeMB": 1024, "envdVersion": "0.5.13",
				"createdAt": "2026-10-29T06:19:37Z", "updatedAt": "2026-10-29T06:19:37Z", "builds": []any{}})
			return
		}
		writeErr(w, r, 404, "template "+ref+" not found")
	}))
	mux.HandleFunc("GET /templates/{templateID}/files/{hash}", s.auth(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeJSON(w, http.StatusCreated, map[string]any{"present": true})
	}))
	mux.HandleFunc("POST /v1/templates/{templateID}/builds/{buildID}/dockerfile", s.auth(func(w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.WriteHeader(http.StatusAccepted)
	}))
	mux.HandleFunc("GET /v1/usage", s.auth(s.usage))
	mux.HandleFunc("GET /v1/billing", s.auth(s.billing))
	mux.HandleFunc("POST /v1/sandboxes/{sandboxID}/preview-links", s.auth(s.previewLink))
	mux.HandleFunc("PUT /v1/sandboxes/{sandboxID}/name", s.auth(s.setName))
	s.mountEnvd(mux)
	return mux
}

// Requests returns the REST requests seen so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Starts returns the envd Start calls seen so far.
func (s *Server) Starts() []StartCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]StartCall(nil), s.starts...)
}

// Signals returns the signal names received through SendSignal.
func (s *Server) Signals() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.signals...)
}

// Sandbox returns a copy of a sandbox record.
func (s *Server) Sandbox(id string) (Sandbox, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sb, ok := s.sandboxes[id]
	if !ok {
		return Sandbox{}, false
	}
	return *sb, true
}

// AddLog appends a log line to a sandbox.
// AddMetric appends one metric sample to a sandbox.
func (s *Server) AddMetric(id string, m Metric) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sb, ok := s.sandboxes[id]; ok {
		sb.Metrics = append(sb.Metrics, m)
	}
}

func (s *Server) AddLog(id string, e LogEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sb, ok := s.sandboxes[id]; ok {
		if e.Fields == nil {
			e.Fields = map[string]string{}
		}
		sb.Logs = append(sb.Logs, e)
	}
}

// SetState forces a sandbox state.
func (s *Server) SetState(id, state string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sb, ok := s.sandboxes[id]; ok {
		sb.State = state
	}
}

// AddSandbox creates a sandbox directly, as if POST /v2/sandboxes ran.
func (s *Server) AddSandbox(templateID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.newSandboxLocked(templateID, 300, nil, nil).ID
}

func (s *Server) newSandboxLocked(templateID string, timeout int32, md, env map[string]string) *Sandbox {
	s.nextID++
	now := time.Now().UTC().Truncate(time.Millisecond)
	sb := &Sandbox{
		ID:         fmt.Sprintf("i%07dfake", s.nextID),
		TemplateID: templateID,
		State:      "running",
		StartedAt:  now.Add(time.Duration(s.nextID) * time.Millisecond),
		EndAt:      now.Add(time.Duration(timeout) * time.Second),
		Metadata:   md,
		EnvVars:    env,
		Token:      fmt.Sprintf("envd-token-%d", s.nextID),
		CPUCount:   2,
		MemoryMB:   512,
		DiskSizeMB: 2048,
	}
	s.sandboxes[sb.ID] = sb
	s.order = append(s.order, sb.ID)
	return sb
}

// ---- REST ----

type errBody struct {
	Code      int32  `json:"code"`
	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, msg string) {
	// Every POST /v2/sandboxes error carries Should-Retry (components.headers).
	if r.Method == http.MethodPost && r.URL.Path == "/v2/sandboxes" {
		w.Header().Set("Should-Retry", "false")
	}
	// The gateway names every request in X-Request-Id.
	w.Header().Set("X-Request-Id", "req-fake-"+strconv.Itoa(status))
	writeJSON(w, status, errBody{Code: int32(status), Message: msg})
}

// Fault is an error answered by something in front of the gateway (a proxy
// or load balancer) instead of the gateway's own response. Such a hop does
// not know the API, so unless Header says otherwise the response carries no
// Should-Retry and a body that is not JSON.
type Fault struct {
	// Method and Path select the requests it applies to, matched exactly.
	Method, Path string
	Status       int
	Header       map[string]string
	// Times is how many matching requests get it before it is used up.
	Times int
	// AfterHandling lets the gateway carry the request out first and then
	// replaces its response, as a proxy whose upstream answered too late
	// does: the sandbox exists, but the caller only sees the fault.
	AfterHandling bool
	// Delay holds the answer this long first, as a create stuck behind a
	// busy engine does. It ends early if the caller goes away.
	Delay time.Duration
	// Body, when set, is sent as a JSON body instead of a proxy's HTML page,
	// so a test can play the gateway's own error (e.g. a capacity 503).
	Body string
}

// InjectFault queues f for the next matching requests.
func (s *Server) InjectFault(f Fault) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.faults = append(s.faults, &f)
}

// takeFaultLocked returns the first unused fault matching r and uses it once.
func (s *Server) takeFaultLocked(r *http.Request) *Fault {
	for _, f := range s.faults {
		if f.Times > 0 && f.Method == r.Method && f.Path == r.URL.Path {
			f.Times--
			c := *f
			return &c
		}
	}
	return nil
}

func writeFault(w http.ResponseWriter, f *Fault) {
	for k, v := range f.Header {
		w.Header().Set(k, v)
	}
	if f.Body != "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.Status)
		_, _ = io.WriteString(w, f.Body)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(f.Status)
	_, _ = fmt.Fprintf(w, "<html><body><h1>%d %s</h1></body></html>\n", f.Status, http.StatusText(f.Status))
}

// discardWriter swallows the gateway's response when a Fault replaces it.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(b []byte) (int, error) { return len(b), nil }
func (d *discardWriter) WriteHeader(int)             {}

// SetBearers replaces the bearer check while the server is serving.
func (s *Server) SetBearers(f func(token string) bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Bearers = f
}

func (s *Server) auth(h func(http.ResponseWriter, *http.Request, []byte)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, Request{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone(), Body: string(body)})
		bearers := s.Bearers
		fault := s.takeFaultLocked(r)
		s.mu.Unlock()
		if fault != nil {
			if fault.Delay > 0 {
				select {
				case <-time.After(fault.Delay):
				case <-r.Context().Done():
					return
				}
			}
			if fault.AfterHandling {
				s.serveAuthed(&discardWriter{h: http.Header{}}, r, body, bearers, h)
			}
			writeFault(w, fault)
			return
		}
		s.serveAuthed(w, r, body, bearers, h)
	}
}

// serveAuthed checks the credential and runs h.
func (s *Server) serveAuthed(w http.ResponseWriter, r *http.Request, body []byte, bearers func(string) bool, h func(http.ResponseWriter, *http.Request, []byte)) {
	bearer, isBearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if isBearer && bearers != nil && r.Header.Get("X-API-Key") == "" {
		if !bearers(bearer) {
			writeErr(w, r, http.StatusUnauthorized, "invalid dashboard token")
			return
		}
	} else if r.Header.Get("X-API-Key") != s.APIKey {
		writeErr(w, r, http.StatusUnauthorized, "Invalid API key")
		return
	}
	h(w, r, body)
}

// sandboxJSON is components.schemas.Sandbox.
func (s *Server) sandboxJSON(sb *Sandbox) map[string]any {
	m := map[string]any{
		"templateID":      sb.TemplateID,
		"sandboxID":       sb.ID,
		"clientID":        "6532622b",
		"envdVersion":     EnvdVersion,
		"envdAccessToken": sb.Token,
		"domain":          s.Domain,
	}
	if sb.Alias != "" {
		m["alias"] = sb.Alias
	}
	return m
}

func (s *Server) listedJSON(sb *Sandbox) map[string]any {
	m := map[string]any{
		"templateID":  sb.TemplateID,
		"sandboxID":   sb.ID,
		"clientID":    "6532622b",
		"startedAt":   sb.StartedAt.Format(time.RFC3339Nano),
		"endAt":       sb.EndAt.Format(time.RFC3339Nano),
		"cpuCount":    sb.CPUCount,
		"memoryMB":    sb.MemoryMB,
		"diskSizeMB":  sb.DiskSizeMB,
		"state":       sb.State,
		"envdVersion": EnvdVersion,
	}
	if sb.Metadata != nil {
		m["metadata"] = sb.Metadata
	}
	return m
}

func (s *Server) detailJSON(sb *Sandbox) map[string]any {
	m := s.listedJSON(sb)
	m["envdAccessToken"] = sb.Token
	m["domain"] = s.Domain
	if sb.AllowNet != nil {
		m["allowInternetAccess"] = *sb.AllowNet
	}
	return m
}

func (s *Server) create(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		TemplateID          *string           `json:"templateID"`
		Timeout             *int32            `json:"timeout"`
		AutoPause           json.RawMessage   `json:"autoPause"`
		AllowInternetAccess *bool             `json:"allow_internet_access"`
		Metadata            map[string]string `json:"metadata"`
		EnvVars             map[string]string `json:"envVars"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, r, 400, "invalid body: "+err.Error())
		return
	}
	// autoPause is a boolean, or the gateway's object {"idleMinutes": n},
	// which also turns pause-on-timeout on.
	var autoPause bool
	var idleMinutes *int
	if ap := bytes.TrimSpace(req.AutoPause); len(ap) > 0 && ap[0] == '{' {
		var o struct {
			IdleMinutes *int `json:"idleMinutes"`
		}
		if err := json.Unmarshal(ap, &o); err != nil {
			writeErr(w, r, 400, "invalid autoPause: "+err.Error())
			return
		}
		autoPause, idleMinutes = true, o.IdleMinutes
	} else if len(ap) > 0 {
		if err := json.Unmarshal(ap, &autoPause); err != nil {
			writeErr(w, r, 400, "invalid autoPause: "+err.Error())
			return
		}
	}
	if req.TemplateID == nil || *req.TemplateID == "" {
		writeErr(w, r, 400, "templateID is required")
		return
	}
	timeout := int32(300)
	if req.Timeout != nil {
		// The gateway's POST /v2/sandboxes: >= 1, or -1 for no limit.
		if *req.Timeout < 1 && *req.Timeout != -1 {
			writeErr(w, r, 400, "timeout must be >= 1, or -1 for no limit")
			return
		}
		timeout = *req.Timeout
	}
	s.mu.Lock()
	sb := s.newSandboxLocked(*req.TemplateID, timeout, req.Metadata, req.EnvVars)
	sb.LastTimeout = timeout
	if timeout == -1 {
		sb.EndAt = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	}
	sb.AllowNet = req.AllowInternetAccess
	sb.AutoPause, sb.IdleMinutes = autoPause, idleMinutes
	out := s.sandboxJSON(sb)
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, out)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request, _ []byte) {
	q := r.URL.Query()
	states := map[string]bool{}
	if v := q.Get("state"); v != "" {
		for _, st := range strings.Split(v, ",") {
			if st != "running" && st != "paused" {
				writeErr(w, r, 400, "invalid state "+st)
				return
			}
			states[st] = true
		}
	}
	var md url.Values
	if v := q.Get("metadata"); v != "" {
		var err error
		md, err = url.ParseQuery(v)
		if err != nil {
			writeErr(w, r, 400, "invalid metadata")
			return
		}
	}
	limit := 100
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			writeErr(w, r, 400, "limit must be 1-100")
			return
		}
		limit = n
	}
	offset := 0
	if v := q.Get("nextToken"); v != "" {
		n, err := strconv.Atoi(strings.TrimPrefix(v, "tok-"))
		if err != nil {
			writeErr(w, r, 400, "invalid nextToken")
			return
		}
		offset = n
	}
	s.mu.Lock()
	var match []*Sandbox
	for _, id := range s.order {
		sb, ok := s.sandboxes[id]
		if !ok {
			continue
		}
		if len(states) > 0 && !states[sb.State] {
			continue
		}
		ok = true
		for k := range md {
			if sb.Metadata[k] != md.Get(k) {
				ok = false
			}
		}
		if ok {
			match = append(match, sb)
		}
	}
	// order: desc by start time (the spec default).
	sort.SliceStable(match, func(i, j int) bool { return match[i].StartedAt.After(match[j].StartedAt) })
	running := 0
	for _, sb := range match {
		if sb.State == "running" {
			running++
		}
	}
	page := []map[string]any{}
	end := offset + limit
	if end > len(match) {
		end = len(match)
	}
	for i := offset; i < end; i++ {
		page = append(page, s.listedJSON(match[i]))
	}
	s.mu.Unlock()
	if end < len(match) {
		w.Header().Set("X-Next-Token", "tok-"+strconv.Itoa(end))
	}
	if len(states) == 0 || states["running"] {
		w.Header().Set("X-Total-Running", strconv.Itoa(running))
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) *Sandbox {
	id := r.PathValue("sandboxID")
	sb, ok := s.sandboxes[id]
	if !ok {
		writeErr(w, r, 404, "sandbox \""+id+"\" doesn't exist or you don't have access to it")
		return nil
	}
	return sb
}

func (s *Server) get(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.mu.Lock()
	sb := s.lookup(w, r)
	if sb == nil {
		s.mu.Unlock()
		return
	}
	out := s.detailJSON(sb)
	s.mu.Unlock()
	writeJSON(w, 200, out)
}

// setName answers like the gateway's PUT /v1/sandboxes/{id}/name: it sets or
// removes the "name" metadata key. Validation is the gateway's; the CLI's own
// is tested by checking that a bad name sends no request at all.
func (s *Server) setName(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		Name *string `json:"name"`
	}
	if json.Unmarshal(body, &in) != nil || in.Name == nil {
		writeErr(w, r, 400, `body must be {"name": "..."}`)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sb := s.lookup(w, r)
	if sb == nil {
		return
	}
	if *in.Name == "" {
		delete(sb.Metadata, "name")
	} else {
		if sb.Metadata == nil {
			sb.Metadata = map[string]string{}
		}
		sb.Metadata["name"] = *in.Name
	}
	writeJSON(w, 200, map[string]string{"sandboxID": sb.ID, "name": sb.Metadata["name"]})
}

// previewLink answers like the gateway's POST /v1/sandboxes/{id}/preview-links
// (the signature is a placeholder; the gateway's own tests check real ones).
func (s *Server) previewLink(w http.ResponseWriter, r *http.Request, body []byte) {
	var in struct {
		Port       int `json:"port"`
		TTLSeconds int `json:"ttlSeconds"`
	}
	if json.Unmarshal(body, &in) != nil || in.Port < 1 || in.Port > 65535 || in.Port == 49983 {
		writeErr(w, r, 400, "port must be 1 to 65535 and not 49983")
		return
	}
	ttl := in.TTLSeconds
	if ttl == 0 {
		ttl = 3600
	}
	s.mu.Lock()
	sb := s.lookup(w, r)
	s.mu.Unlock()
	if sb == nil {
		return
	}
	exp := time.Now().Add(time.Duration(ttl) * time.Second).UTC().Truncate(time.Second)
	writeJSON(w, 201, map[string]any{
		"url":       fmt.Sprintf("https://%d-%s.sbx.test/?anona_exp=%d&anona_sig=fake", in.Port, sb.ID, exp.Unix()),
		"sandboxID": sb.ID, "port": in.Port, "expiresAt": exp.Format(time.RFC3339),
	})
}

func (s *Server) kill(w http.ResponseWriter, r *http.Request, _ []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sb := s.lookup(w, r)
	if sb == nil {
		return
	}
	delete(s.sandboxes, sb.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pause(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Memory *bool `json:"memory"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeErr(w, r, 400, "invalid body")
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sb := s.lookup(w, r)
	if sb == nil {
		return
	}
	if sb.State == "paused" {
		writeErr(w, r, 409, "sandbox is already paused")
		return
	}
	sb.State = "paused"
	sb.PausedWith = req.Memory
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) connect(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Timeout *int32 `json:"timeout"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeErr(w, r, 400, "invalid body")
			return
		}
	}
	timeout := int32(300)
	if req.Timeout != nil {
		timeout = *req.Timeout
	}
	s.mu.Lock()
	sb := s.lookup(w, r)
	if sb == nil {
		s.mu.Unlock()
		return
	}
	status := 200
	if sb.State == "paused" {
		sb.State = "running"
		status = 201
	}
	// TTL is only extended.
	if end := time.Now().UTC().Add(time.Duration(timeout) * time.Second); end.After(sb.EndAt) {
		sb.EndAt = end
	}
	out := s.sandboxJSON(sb)
	s.mu.Unlock()
	writeJSON(w, status, out)
}

func (s *Server) timeout(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Timeout *int32 `json:"timeout"`
	}
	// -1 is the gateway's "no deadline" (handlers/sandboxes/longrun.go).
	if err := json.Unmarshal(body, &req); err != nil || req.Timeout == nil || (*req.Timeout < 0 && *req.Timeout != -1) {
		writeErr(w, r, 400, "timeout is required and must be >= 0, or -1 for no limit")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sb := s.lookup(w, r)
	if sb == nil {
		return
	}
	sb.LastTimeout = *req.Timeout
	sb.EndAt = time.Now().UTC().Add(time.Duration(*req.Timeout) * time.Second)
	if *req.Timeout == -1 {
		sb.EndAt = time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) fork(w http.ResponseWriter, r *http.Request, body []byte) {
	var req struct {
		Timeout *int32 `json:"timeout"`
		Count   *int32 `json:"count"`
	}
	if len(body) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeErr(w, r, 400, "invalid body")
			return
		}
	}
	count, timeout := int32(1), int32(15)
	if req.Count != nil {
		count = *req.Count
	}
	if req.Timeout != nil {
		timeout = *req.Timeout
	}
	s.mu.Lock()
	src := s.lookup(w, r)
	if src == nil {
		s.mu.Unlock()
		return
	}
	if src.State != "running" {
		s.mu.Unlock()
		writeErr(w, r, 409, "sandbox is not running")
		return
	}
	var out []map[string]any
	for i := int32(0); i < count; i++ {
		sb := s.newSandboxLocked(src.TemplateID, timeout, src.Metadata, src.EnvVars)
		sb.ForkedFrom = src.ID
		out = append(out, map[string]any{"sandbox": s.sandboxJSON(sb)})
	}
	s.mu.Unlock()
	writeJSON(w, http.StatusCreated, out)
}

// metrics serves GET /sandboxes/{sandboxID}/metrics: the samples added with
// AddMetric, inside the optional start and end window (Unix seconds).
func (s *Server) metrics(w http.ResponseWriter, r *http.Request, _ []byte) {
	q := r.URL.Query()
	window := [2]int64{}
	for i, name := range []string{"start", "end"} {
		if v := q.Get(name); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				writeErr(w, r, 400, "invalid "+name)
				return
			}
			window[i] = n
		}
	}
	s.mu.Lock()
	sb := s.lookup(w, r)
	if sb == nil {
		s.mu.Unlock()
		return
	}
	all := append([]Metric(nil), sb.Metrics...)
	s.mu.Unlock()
	out := []Metric{}
	for _, m := range all {
		if (window[0] > 0 && m.TimestampUnix < window[0]) || (window[1] > 0 && m.TimestampUnix > window[1]) {
			continue
		}
		out = append(out, m)
	}
	writeJSON(w, 200, out)
}

var levelRank = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}

func (s *Server) logs(w http.ResponseWriter, r *http.Request, _ []byte) {
	q := r.URL.Query()
	var cursor int64
	if v := q.Get("cursor"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			writeErr(w, r, 400, "invalid cursor")
			return
		}
		cursor = n
	}
	limit := 1000
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > 1000 {
			writeErr(w, r, 400, "invalid limit")
			return
		}
		limit = n
	}
	dir := q.Get("direction")
	if dir != "" && dir != "forward" && dir != "backward" {
		writeErr(w, r, 400, "invalid direction")
		return
	}
	minLevel := 0
	if v := q.Get("level"); v != "" {
		lv, ok := levelRank[v]
		if !ok {
			writeErr(w, r, 400, "invalid level")
			return
		}
		minLevel = lv
	}
	search := q.Get("search")
	s.mu.Lock()
	sb := s.lookup(w, r)
	if sb == nil {
		s.mu.Unlock()
		return
	}
	entries := append([]LogEntry(nil), sb.Logs...)
	s.mu.Unlock()
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Timestamp.Before(entries[j].Timestamp) })
	if dir == "backward" {
		for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
			entries[i], entries[j] = entries[j], entries[i]
		}
	}
	out := []LogEntry{}
	for _, e := range entries {
		ms := e.Timestamp.UnixMilli()
		if dir == "backward" {
			if q.Get("cursor") != "" && ms > cursor {
				continue
			}
		} else if ms < cursor {
			continue
		}
		if levelRank[e.Level] < minLevel {
			continue
		}
		if search != "" && !strings.Contains(e.Message, search) {
			continue
		}
		out = append(out, e)
		if len(out) == limit {
			break
		}
	}
	writeJSON(w, 200, map[string]any{"logs": out})
}

// ---- envd (Connect) ----

func (s *Server) mountEnvd(mux *http.ServeMux) {
	var fds descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(processDescriptorSet, &fds); err != nil {
		panic(err)
	}
	files, err := protodesc.NewFiles(&fds)
	if err != nil {
		panic(err)
	}
	d, err := files.FindDescriptorByName("process.Process")
	if err != nil {
		panic(err)
	}
	svc := d.(protoreflect.ServiceDescriptor)
	start := svc.Methods().ByName("Start")
	signal := svc.Methods().ByName("SendSignal")
	init := connect.WithRequestInitializer(func(spec connect.Spec, msg any) error {
		md, ok := spec.Schema.(protoreflect.MethodDescriptor)
		if !ok {
			return errors.New("no schema")
		}
		*msg.(*dynamicpb.Message) = *dynamicpb.NewMessage(md.Input())
		return nil
	})
	startPath := "/process.Process/Start"
	mux.Handle(startPath, connect.NewServerStreamHandler(startPath,
		func(ctx context.Context, req *connect.Request[dynamicpb.Message], stream *connect.ServerStream[dynamicpb.Message]) error {
			return s.envdStart(ctx, req, stream, start.Output())
		}, connect.WithSchema(start), init))
	signalPath := "/process.Process/SendSignal"
	mux.Handle(signalPath, connect.NewUnaryHandler(signalPath,
		func(ctx context.Context, req *connect.Request[dynamicpb.Message]) (*connect.Response[dynamicpb.Message], error) {
			return s.envdSignal(req, signal.Output())
		}, connect.WithSchema(signal), init))
	for name, fn := range map[string]func(*connect.Request[dynamicpb.Message], protoreflect.MessageDescriptor) (*connect.Response[dynamicpb.Message], error){
		"SendInput": s.envdSendInput, "CloseStdin": s.envdCloseStdin, "List": s.envdList,
	} {
		md := svc.Methods().ByName(protoreflect.Name(name))
		path := "/process.Process/" + name
		fn := fn
		mux.Handle(path, connect.NewUnaryHandler(path,
			func(ctx context.Context, req *connect.Request[dynamicpb.Message]) (*connect.Response[dynamicpb.Message], error) {
				return fn(req, md.Output())
			}, connect.WithSchema(md), init))
	}
}

// selectedPID reads a request's process selector (pid only).
func selectedPID(msg *dynamicpb.Message) uint32 {
	sel := msg.Get(field(msg, "process")).Message()
	return uint32(sel.Get(field(sel, "pid")).Uint())
}

func (s *Server) envdSendInput(req *connect.Request[dynamicpb.Message], outDesc protoreflect.MessageDescriptor) (*connect.Response[dynamicpb.Message], error) {
	if err := s.checkEnvdAuth(req.Header()); err != nil {
		return nil, err
	}
	msg := req.Msg
	pid := selectedPID(msg)
	in := msg.Get(field(msg, "input")).Message()
	s.mu.Lock()
	w, ok := s.stdins[pid]
	s.mu.Unlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("process %d not found or has no stdin", pid))
	}
	if _, err := w.Write(in.Get(field(in, "stdin")).Bytes()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(dynamicpb.NewMessage(outDesc)), nil
}

func (s *Server) envdCloseStdin(req *connect.Request[dynamicpb.Message], outDesc protoreflect.MessageDescriptor) (*connect.Response[dynamicpb.Message], error) {
	if err := s.checkEnvdAuth(req.Header()); err != nil {
		return nil, err
	}
	pid := selectedPID(req.Msg)
	s.mu.Lock()
	w, ok := s.stdins[pid]
	delete(s.stdins, pid)
	s.mu.Unlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("process %d not found or has no stdin", pid))
	}
	_ = w.Close()
	return connect.NewResponse(dynamicpb.NewMessage(outDesc)), nil
}

func (s *Server) envdList(req *connect.Request[dynamicpb.Message], outDesc protoreflect.MessageDescriptor) (*connect.Response[dynamicpb.Message], error) {
	if err := s.checkEnvdAuth(req.Header()); err != nil {
		return nil, err
	}
	out := dynamicpb.NewMessage(outDesc)
	list := out.Mutable(field(out, "processes")).List()
	s.mu.Lock()
	pids := make([]uint32, 0, len(s.procCalls))
	for pid := range s.procCalls {
		pids = append(pids, pid)
	}
	sort.Slice(pids, func(i, j int) bool { return pids[i] < pids[j] })
	for _, pid := range pids {
		call := s.procCalls[pid]
		info := list.NewElement().Message()
		info.Set(field(info, "pid"), protoreflect.ValueOfUint32(pid))
		cfg := info.Mutable(field(info, "config")).Message()
		cfg.Set(field(cfg, "cmd"), protoreflect.ValueOfString(call.Cmd))
		args := cfg.Mutable(field(cfg, "args")).List()
		for _, a := range call.Args {
			args.Append(protoreflect.ValueOfString(a))
		}
		list.Append(protoreflect.ValueOfMessage(info))
	}
	s.mu.Unlock()
	return connect.NewResponse(out), nil
}

func (s *Server) checkEnvdAuth(h http.Header) error {
	id := h.Get("E2b-Sandbox-Id")
	s.mu.Lock()
	defer s.mu.Unlock()
	sb, ok := s.sandboxes[id]
	if !ok {
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("sandbox %q not found", id))
	}
	if h.Get("X-Access-Token") != sb.Token {
		return connect.NewError(connect.CodeUnauthenticated, errors.New("invalid access token"))
	}
	if h.Get("E2b-Sandbox-Port") != "49983" {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("wrong E2b-Sandbox-Port"))
	}
	return nil
}

func field(m protoreflect.Message, name string) protoreflect.FieldDescriptor {
	return m.Descriptor().Fields().ByName(protoreflect.Name(name))
}

func (s *Server) envdStart(ctx context.Context, req *connect.Request[dynamicpb.Message], stream *connect.ServerStream[dynamicpb.Message], outDesc protoreflect.MessageDescriptor) error {
	if err := s.checkEnvdAuth(req.Header()); err != nil {
		return err
	}
	msg := req.Msg
	pc := msg.Get(field(msg, "process")).Message()
	call := StartCall{
		SandboxID:   req.Header().Get("E2b-Sandbox-Id"),
		AccessToken: req.Header().Get("X-Access-Token"),
		Port:        req.Header().Get("E2b-Sandbox-Port"),
		ContentType: req.Header().Get("Content-Type"),
		Auth:        req.Header().Get("Authorization"),
		Cmd:         pc.Get(field(pc, "cmd")).String(),
		Envs:        map[string]string{},
	}
	args := pc.Get(field(pc, "args")).List()
	for i := 0; i < args.Len(); i++ {
		call.Args = append(call.Args, args.Get(i).String())
	}
	pc.Get(field(pc, "envs")).Map().Range(func(k protoreflect.MapKey, v protoreflect.Value) bool {
		call.Envs[k.String()] = v.String()
		return true
	})
	if pc.Has(field(pc, "cwd")) {
		call.Cwd = pc.Get(field(pc, "cwd")).String()
	}
	call.Stdin = true
	if fd := field(msg, "stdin"); msg.Has(fd) {
		call.Stdin = msg.Get(fd).Bool()
	}
	s.mu.Lock()
	s.starts = append(s.starts, call)
	s.mu.Unlock()

	if call.Cmd != "/bin/bash" || len(call.Args) != 3 || call.Args[0] != "-l" || call.Args[1] != "-c" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unexpected command %q %q", call.Cmd, call.Args))
	}
	// Run the command for real. --noprofile/--norc keep the test machine's
	// shell profile out of the output; the request itself is checked above.
	cmd := exec.Command("/bin/bash", "--noprofile", "--norc", "-c", call.Args[2])
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Dir = call.Cwd
	cmd.Env = []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=/tmp"}
	for k, v := range call.Envs {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	stdout, _ := cmd.StdoutPipe()
	stderr, _ := cmd.StderrPipe()
	var stdin io.WriteCloser
	if call.Stdin {
		stdin, _ = cmd.StdinPipe()
	}
	if err := cmd.Start(); err != nil {
		return connect.NewError(connect.CodeInternal, err)
	}
	pid := uint32(cmd.Process.Pid)
	s.mu.Lock()
	s.procs[pid] = cmd
	s.procCalls[pid] = call
	if stdin != nil {
		s.stdins[pid] = stdin
	}
	s.mu.Unlock()

	evDesc := outDesc.Fields().ByName("event").Message()
	var sendMu sync.Mutex
	send := func(build func(ev *dynamicpb.Message)) error {
		ev := dynamicpb.NewMessage(evDesc)
		build(ev)
		out := dynamicpb.NewMessage(outDesc)
		out.Set(outDesc.Fields().ByName("event"), protoreflect.ValueOfMessage(ev))
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(out)
	}
	sub := func(ev *dynamicpb.Message, name string) *dynamicpb.Message {
		fd := evDesc.Fields().ByName(protoreflect.Name(name))
		m := dynamicpb.NewMessage(fd.Message())
		ev.Set(fd, protoreflect.ValueOfMessage(m))
		return m
	}
	if err := send(func(ev *dynamicpb.Message) {
		st := sub(ev, "start")
		st.Set(field(st, "pid"), protoreflect.ValueOfUint32(pid))
	}); err != nil {
		return err
	}
	var wg sync.WaitGroup
	pump := func(r interface{ Read([]byte) (int, error) }, name string) {
		defer wg.Done()
		buf := make([]byte, 32<<10)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				chunk := append([]byte(nil), buf[:n]...)
				_ = send(func(ev *dynamicpb.Message) {
					d := sub(ev, "data")
					d.Set(field(d, name), protoreflect.ValueOfBytes(chunk))
				})
			}
			if err != nil {
				return
			}
		}
	}
	wg.Add(2)
	go pump(stdout, "stdout")
	go pump(stderr, "stderr")
	wg.Wait()
	_ = cmd.Wait()
	s.mu.Lock()
	delete(s.procs, pid)
	delete(s.procCalls, pid)
	delete(s.stdins, pid)
	s.mu.Unlock()
	ps := cmd.ProcessState
	// Same fields upstream envd fills from os.ProcessState.
	return send(func(ev *dynamicpb.Message) {
		e := sub(ev, "end")
		e.Set(field(e, "exit_code"), protoreflect.ValueOfInt32(int32(ps.ExitCode())))
		e.Set(field(e, "exited"), protoreflect.ValueOfBool(ps.Exited()))
		e.Set(field(e, "status"), protoreflect.ValueOfString(ps.String()))
	})
}

func (s *Server) envdSignal(req *connect.Request[dynamicpb.Message], outDesc protoreflect.MessageDescriptor) (*connect.Response[dynamicpb.Message], error) {
	if err := s.checkEnvdAuth(req.Header()); err != nil {
		return nil, err
	}
	msg := req.Msg
	sel := msg.Get(field(msg, "process")).Message()
	pid := uint32(sel.Get(field(sel, "pid")).Uint())
	sigFD := field(msg, "signal")
	num := msg.Get(sigFD).Enum()
	name := string(sigFD.Enum().Values().ByNumber(num).Name())
	s.mu.Lock()
	s.signals = append(s.signals, name)
	cmd, ok := s.procs[pid]
	s.mu.Unlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("process %d not found", pid))
	}
	var sig syscall.Signal
	switch name {
	case "SIGNAL_SIGTERM":
		sig = syscall.SIGTERM
	case "SIGNAL_SIGKILL":
		sig = syscall.SIGKILL
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unsupported signal %s", name))
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
	return connect.NewResponse(dynamicpb.NewMessage(outDesc)), nil
}
