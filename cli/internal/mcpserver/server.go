// Package mcpserver exposes Anona Compute to AI agents as a Model Context
// Protocol server (github.com/modelcontextprotocol/go-sdk). Every call goes
// through the gateway with the CLI's own credential, so the gateway's
// tenant, role and API key scope checks apply unchanged; the server adds no
// access of its own. Viewer sessions (and --read-only) get the read-only
// tools only.
package mcpserver

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/envd"
)

// SSHConfig is what get_ssh_config returns.
type SSHConfig struct {
	Host           string `json:"host" jsonschema:"ssh host name to use (ssh <host>)"`
	User           string `json:"user" jsonschema:"login user inside the sandbox"`
	Config         string `json:"config" jsonschema:"ssh_config block; append it to ~/.ssh/config or pass it with ssh -F"`
	Command        string `json:"command" jsonschema:"one-off command that opens a shell without editing ssh_config"`
	ExpiresAt      string `json:"expires_at" jsonschema:"when the SSH certificate in the config expires (call again to renew)"`
	IdentityFile   string `json:"identity_file"`
	CertificateFle string `json:"certificate_file"`
}

// Config wires the server to the CLI.
type Config struct {
	API *api.Client
	// Envd returns an envd client for a running sandbox.
	Envd func(id, accessToken, envdVersion string, domain *string, user string) *envd.Client
	// Whoami describes the credential (shown by the whoami tool).
	Whoami func(ctx context.Context) (map[string]any, error)
	// SSH signs a certificate and returns an ssh_config block; nil hides
	// the tool.
	SSH func(ctx context.Context, id string) (*SSHConfig, error)
	// ReadOnly registers only the tools that do not change sandboxes.
	ReadOnly bool
	Version  string
}

type server struct {
	cfg   Config
	procs procs
}

// New builds the MCP server.
func New(cfg Config) *mcp.Server {
	s := &server{cfg: cfg, procs: procs{m: map[string]*proc{}}}
	instructions := `Anona Compute runs E2B-compatible microVM sandboxes. Create a sandbox (create_sandbox), run commands in it (run_command, start_process + read_process_output), move files (write_file, read_file, list_files) and kill it when done (kill_sandbox): sandboxes cost money while they run. Sandbox IDs come from create_sandbox or list_sandboxes. Every call uses the signed-in Anona account and is checked by the Anona gateway.`
	if cfg.ReadOnly {
		instructions += " This session is read-only (viewer role or --read-only): only listing and inspecting sandboxes is available."
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "anona-compute", Title: "Anona Compute", Version: cfg.Version},
		&mcp.ServerOptions{Instructions: instructions})
	s.register(srv)
	return srv
}

func ptr[T any](v T) *T { return &v }

var (
	readOnly    = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
	additive    = &mcp.ToolAnnotations{DestructiveHint: ptr(false), OpenWorldHint: ptr(true)}
	idempotent  = &mcp.ToolAnnotations{DestructiveHint: ptr(false), IdempotentHint: true, OpenWorldHint: ptr(false)}
	destructive = &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(true)}
)

func annotate(a *mcp.ToolAnnotations, title string) *mcp.ToolAnnotations {
	c := *a
	c.Title = title
	return &c
}

// schema infers T's input schema and lets mod add enums and bounds.
func schema[T any](mod func(props map[string]*jsonschema.Schema)) *jsonschema.Schema {
	sc, err := jsonschema.For[T](nil)
	if err != nil {
		panic(err)
	}
	if mod != nil {
		mod(sc.Properties)
	}
	return sc
}

func enum(vals ...string) []any {
	out := make([]any, len(vals))
	for i, v := range vals {
		out[i] = v
	}
	return out
}

func fptr(f float64) *float64 { return &f }

func (s *server) register(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "whoami",
		Description: "Show the Anona account this server acts as: user, organization and role for a browser login, or the masked API key. Use it to check which organization new sandboxes will belong to.",
		Annotations: annotate(readOnly, "Who am I"),
	}, s.whoami)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_sandboxes",
		Description: "List the organization's sandboxes with their state (running or paused), template, start time, end time and metadata.",
		Annotations: annotate(readOnly, "List sandboxes"),
		InputSchema: schema[listIn](func(p map[string]*jsonschema.Schema) {
			p["state"].Enum = enum("running", "paused", "all")
			p["limit"].Minimum, p["limit"].Maximum = fptr(1), fptr(100)
		}),
	}, s.listSandboxes)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_sandbox",
		Description: "Get one sandbox: state, template, CPU and memory, start and end time (end_at absent means no time limit) and metadata.",
		Annotations: annotate(readOnly, "Get sandbox"),
	}, s.getSandbox)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_live_view_url",
		Description: "Mint a short-lived live-view link to watch a sandbox in a browser: mode desktop (the desktop template's screen), dom (a browser agent's page) or terminal (a new shell; needs control). " +
			"control=true lets the viewer type and click; viewers can only get view links. Share the URL with the user; the token is in the URL fragment and expires after ttl_seconds.",
		Annotations: annotate(readOnly, "Live view URL"),
		InputSchema: schema[viewIn](func(p map[string]*jsonschema.Schema) {
			p["mode"].Enum = enum("desktop", "dom", "terminal")
			p["ttl_seconds"].Minimum, p["ttl_seconds"].Maximum = fptr(30), fptr(600)
		}),
	}, s.liveView)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_templates",
		Description: "List the organization's own templates (built with `anona-compute template build` or the E2B SDK's Template.build): name, template ID, build status (ready, building, waiting or error), CPU and memory. Pass a template name to create_sandbox to start a sandbox from it; the platform templates base, python, node, code-interpreter, desktop and agent-desktop are always available and are not listed.",
		Annotations: annotate(readOnly, "List templates"),
	}, s.listTemplates)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_sandbox_logs",
		Description: "Read a running sandbox's log: the lines its processes printed (stdout and stderr) and what envd did, newest last. " +
			"Use it to see why something in the sandbox failed after the fact, instead of re-running it. " +
			"level filters by minimum severity (default info; debug adds envd's own chatter), search keeps only lines containing that text.",
		Annotations: annotate(readOnly, "Sandbox logs"),
		InputSchema: schema[logsIn](func(p map[string]*jsonschema.Schema) {
			p["level"].Enum = enum("debug", "info", "warn", "error")
			p["limit"].Minimum, p["limit"].Maximum = fptr(1), fptr(1000)
		}),
	}, s.getLogs)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_sandbox_metrics",
		Description: "Read a running sandbox's CPU, memory and disk use, measured inside the sandbox by its own envd: the latest sample plus the samples the gateway has kept. " +
			"Use it to see whether a sandbox is out of memory or disk before blaming the program.",
		Annotations: annotate(readOnly, "Sandbox metrics"),
	}, s.getMetrics)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_usage",
		Description: "Get the organization's usage and cost for a billing period (default the current month): meters with quantities and amounts in USD, " +
			"totals (rated, not yet rated, due) with a projection for the current month, and the most expensive sandboxes. " +
			"Any role may read it; an API key needs the usage:read scope.",
		Annotations: annotate(readOnly, "Usage and cost"),
		InputSchema: schema[usageIn](func(p map[string]*jsonschema.Schema) {
			p["period"].Pattern = `^(current|\d{4}-\d{2})$`
		}),
	}, s.getUsage)
	if s.cfg.ReadOnly {
		return
	}

	mcp.AddTool(srv, &mcp.Tool{
		Name: "create_sandbox",
		Description: "Create and start a new sandbox (a microVM) and return its sandbox_id. Templates: base (Ubuntu with bash, git, curl), python, node, code-interpreter, desktop (XFCE desktop for computer use), agent-desktop (the desktop plus the coding-agent CLIs), or a custom template. " +
			"timeout_seconds is the lifetime (default 300); -1 means no time limit (the sandbox runs until killed, if the plan allows it). Kill sandboxes you no longer need.",
		Annotations: annotate(additive, "Create sandbox"),
		InputSchema: schema[createIn](func(p map[string]*jsonschema.Schema) {
			p["timeout_seconds"].Minimum = fptr(-1)
		}),
	}, s.createSandbox)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "kill_sandbox",
		Description: "Kill a sandbox and delete it with all its files and processes. This cannot be undone; only call it for sandboxes you created or were asked to kill.",
		Annotations: annotate(destructive, "Kill sandbox"),
	}, s.killSandbox)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "pause_sandbox",
		Description: "Pause a running sandbox: its memory and files are saved and it stops using CPU. resume_sandbox continues it where it left off. Running commands are frozen, not ended.",
		Annotations: annotate(idempotent, "Pause sandbox"),
	}, s.pauseSandbox)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "resume_sandbox",
		Description: "Resume a paused sandbox (a running one is returned unchanged) and set its lifetime to timeout_seconds from now (default 300, -1 for no time limit).",
		Annotations: annotate(idempotent, "Resume sandbox"),
		InputSchema: schema[resumeIn](func(p map[string]*jsonschema.Schema) { p["timeout_seconds"].Minimum = fptr(-1) }),
	}, s.resumeSandbox)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "extend_sandbox",
		Description: "Set a running sandbox to end timeout_seconds from now (it can shorten as well as extend the lifetime); -1 removes the time limit.",
		Annotations: annotate(idempotent, "Extend sandbox lifetime"),
		InputSchema: schema[extendIn](func(p map[string]*jsonschema.Schema) { p["timeout_seconds"].Minimum = fptr(-1) }),
	}, s.extendSandbox)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "run_command",
		Description: "Run a shell command in a running sandbox and wait for it to finish (bash -l -c <command>). Returns stdout, stderr and the exit code. " +
			"The command is stopped after timeout_seconds (default 60, max 3600) and timed_out is set. For servers or long jobs use start_process instead. Output over 256 KiB per stream keeps only the end.",
		Annotations: annotate(additive, "Run command"),
		InputSchema: schema[runIn](func(p map[string]*jsonschema.Schema) {
			p["timeout_seconds"].Minimum, p["timeout_seconds"].Maximum = fptr(1), fptr(3600)
		}),
	}, s.runCommand)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "start_process",
		Description: "Start a long-running command in the background (a dev server, a build, a watcher) and return its pid at once. " +
			"Read its output with read_process_output and stop it with kill_process. Output is collected by this MCP server while it runs (last 4 MiB per stream).",
		Annotations: annotate(additive, "Start background process"),
	}, s.startProcess)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "read_process_output",
		Description: "Read new output of a process started with start_process. Pass back the stdout_offset and stderr_offset from the previous call to get only what is new. " +
			"wait_seconds (max 60) waits for new output or for the process to exit. Returns running, and exit_code once it has exited.",
		Annotations: annotate(readOnly, "Read process output"),
		InputSchema: schema[readProcIn](func(p map[string]*jsonschema.Schema) {
			p["wait_seconds"].Minimum, p["wait_seconds"].Maximum = fptr(0), fptr(60)
			p["max_bytes"].Minimum, p["max_bytes"].Maximum = fptr(1), fptr(1<<20)
		}),
	}, s.readProcess)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "kill_process",
		Description: "Stop a process in a sandbox by pid: SIGTERM (default, lets it clean up) or SIGKILL.",
		Annotations: annotate(destructive, "Kill process"),
		InputSchema: schema[killProcIn](func(p map[string]*jsonschema.Schema) { p["signal"].Enum = enum("SIGTERM", "SIGKILL") }),
	}, s.killProcess)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "read_file",
		Description: "Read a file from a running sandbox. Text comes back as is; binary files (or encoding=base64) come back base64 encoded. max_bytes defaults to 1 MiB (max 10 MiB).",
		Annotations: annotate(readOnly, "Read file"),
		InputSchema: schema[readFileIn](func(p map[string]*jsonschema.Schema) {
			p["encoding"].Enum = enum("text", "base64")
			p["max_bytes"].Minimum, p["max_bytes"].Maximum = fptr(1), fptr(10<<20)
		}),
	}, s.readFile)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "write_file",
		Description: "Write a file in a running sandbox, creating parent directories and replacing an existing file. content is text, or base64 with encoding=base64 for binary data.",
		Annotations: annotate(&mcp.ToolAnnotations{DestructiveHint: ptr(true), IdempotentHint: true, OpenWorldHint: ptr(false)}, "Write file"),
		InputSchema: schema[writeFileIn](func(p map[string]*jsonschema.Schema) { p["encoding"].Enum = enum("text", "base64") }),
	}, s.writeFile)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_files",
		Description: "List a directory in a running sandbox: name, path, type (file, dir, symlink), size, permissions and modification time. depth 1 (default) lists only the directory itself.",
		Annotations: annotate(readOnly, "List files"),
		InputSchema: schema[listFilesIn](func(p map[string]*jsonschema.Schema) { p["depth"].Minimum, p["depth"].Maximum = fptr(1), fptr(5) }),
	}, s.listFiles)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "desktop_screenshot",
		Description: "Take a PNG screenshot of a desktop sandbox's X display (desktop template, after the desktop was started, for example by the e2b-desktop SDK or Xvfb). Fails clearly when no X display is running.",
		Annotations: annotate(readOnly, "Desktop screenshot"),
	}, s.screenshot)
	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_preview_url",
		Description: "Make a shareable browser link to one port of a running sandbox, for example a dev server or web app the agent started on port 3000. " +
			"Anyone with the URL can open that port (only that port) until it expires after ttl_seconds (default 3600, max 86400); envd's port 49983 cannot be shared. Give the URL to the user.",
		Annotations: annotate(additive, "Preview URL"),
		InputSchema: schema[previewIn](func(p map[string]*jsonschema.Schema) {
			p["port"].Minimum, p["port"].Maximum = fptr(1), fptr(65535)
			p["ttl_seconds"].Minimum, p["ttl_seconds"].Maximum = fptr(60), fptr(86400)
		}),
	}, s.preview)
	if s.cfg.SSH != nil {
		mcp.AddTool(srv, &mcp.Tool{
			Name: "get_ssh_config",
			Description: "Prepare SSH access to a sandbox from this machine: signs a 10-minute certificate for a local key and returns an ssh_config block plus a one-off ssh command. " +
				"scp, rsync and editors (VS Code Remote-SSH) work with the config. The sandbox template must run sshd (base and desktop do).",
			Annotations: annotate(additive, "SSH config"),
		}, s.ssh)
	}
}

// ---- inputs and outputs ----

type idIn struct {
	SandboxID string `json:"sandbox_id" jsonschema:"the sandbox ID, from create_sandbox or list_sandboxes"`
}

type listIn struct {
	State    string            `json:"state,omitempty" jsonschema:"which sandboxes: running, paused or all (default all)"`
	Metadata map[string]string `json:"metadata,omitempty" jsonschema:"only sandboxes whose metadata has all of these key/value pairs"`
	Limit    int               `json:"limit,omitempty" jsonschema:"at most this many sandboxes (default 100)"`
}

type sandboxOut struct {
	SandboxID   string            `json:"sandbox_id"`
	TemplateID  string            `json:"template_id"`
	Alias       string            `json:"alias,omitempty"`
	State       string            `json:"state" jsonschema:"running or paused"`
	StartedAt   string            `json:"started_at"`
	EndAt       *string           `json:"end_at,omitempty" jsonschema:"when the sandbox ends; absent means no time limit"`
	CPUCount    int32             `json:"cpu_count"`
	MemoryMB    int32             `json:"memory_mb"`
	DiskSizeMB  int32             `json:"disk_size_mb,omitempty"`
	EnvdVersion string            `json:"envd_version,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

type listOut struct {
	Sandboxes []sandboxOut `json:"sandboxes"`
	More      bool         `json:"more" jsonschema:"true when more sandboxes exist than were returned"`
}

type templateOut struct {
	TemplateID string   `json:"template_id"`
	Names      []string `json:"names" jsonschema:"names to pass as create_sandbox's template"`
	Status     string   `json:"status" jsonschema:"newest build: ready, building, waiting or error"`
	Phase      string   `json:"phase,omitempty" jsonschema:"newest build's pipeline step (queued, building, pushing, registering, ready, failed)"`
	CPUCount   int      `json:"cpu_count"`
	MemoryMB   int      `json:"memory_mb"`
	Builds     int      `json:"builds"`
	UpdatedAt  string   `json:"updated_at"`
}

type listTemplatesOut struct {
	Templates []templateOut `json:"templates"`
}

func (s *server) listTemplates(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listTemplatesOut, error) {
	ts, err := s.cfg.API.ListTemplates(ctx)
	if err != nil {
		return nil, listTemplatesOut{}, err
	}
	out := listTemplatesOut{Templates: []templateOut{}}
	for _, t := range ts {
		names := t.Names
		if names == nil {
			names = []string{}
		}
		out.Templates = append(out.Templates, templateOut{TemplateID: t.TemplateID, Names: names, Status: t.BuildStatus, Phase: t.Phase,
			CPUCount: t.CPUCount, MemoryMB: t.MemoryMB, Builds: t.BuildCount, UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339)})
	}
	return nil, out, nil
}

type viewIn struct {
	SandboxID  string `json:"sandbox_id" jsonschema:"the sandbox ID"`
	Mode       string `json:"mode,omitempty" jsonschema:"desktop (default), dom or terminal"`
	Control    bool   `json:"control,omitempty" jsonschema:"allow input (keyboard and mouse); needs a role that may change sandboxes"`
	TTLSeconds int    `json:"ttl_seconds,omitempty" jsonschema:"link lifetime in seconds, 30 to 600 (default 600)"`
}

type previewIn struct {
	SandboxID  string `json:"sandbox_id" jsonschema:"the sandbox ID"`
	Port       int    `json:"port" jsonschema:"the sandbox port to share, for example 3000"`
	TTLSeconds int    `json:"ttl_seconds,omitempty" jsonschema:"link lifetime in seconds, 60 to 86400 (default 3600)"`
}

type previewOut struct {
	URL       string `json:"url" jsonschema:"open this URL in a browser"`
	Port      int    `json:"port"`
	ExpiresAt string `json:"expires_at"`
}

type viewOut struct {
	URL       string `json:"url" jsonschema:"open this URL in a browser"`
	ExpiresAt string `json:"expires_at"`
	Scope     string `json:"scope" jsonschema:"view or control"`
	Channel   string `json:"channel"`
}

type createIn struct {
	Template         string            `json:"template,omitempty" jsonschema:"template ID or alias (default base)"`
	TimeoutSeconds   *int32            `json:"timeout_seconds,omitempty" jsonschema:"lifetime in seconds (default 300); -1 for no time limit"`
	Internet         *bool             `json:"internet,omitempty" jsonschema:"allow outbound internet access (default true)"`
	AutoPause        bool              `json:"auto_pause,omitempty" jsonschema:"pause instead of kill when the lifetime runs out"`
	IdlePauseMinutes *int              `json:"idle_pause_minutes,omitempty" jsonschema:"pause the sandbox after this many minutes with nothing using it (1 to 10080); 0 turns it off. Unrelated to the lifetime"`
	Metadata         map[string]string `json:"metadata,omitempty" jsonschema:"labels to find the sandbox by later (list_sandboxes metadata filter)"`
	EnvVars          map[string]string `json:"env_vars,omitempty" jsonschema:"environment variables for every command in the sandbox"`
}

type createOut struct {
	SandboxID   string `json:"sandbox_id"`
	TemplateID  string `json:"template_id"`
	EnvdVersion string `json:"envd_version"`
}

type resumeIn struct {
	SandboxID      string `json:"sandbox_id" jsonschema:"the sandbox ID"`
	TimeoutSeconds *int32 `json:"timeout_seconds,omitempty" jsonschema:"new lifetime from now in seconds (default 300); -1 for no time limit"`
}

type extendIn struct {
	SandboxID      string `json:"sandbox_id" jsonschema:"the sandbox ID"`
	TimeoutSeconds int32  `json:"timeout_seconds" jsonschema:"seconds from now until the sandbox ends; -1 for no time limit"`
}

type statusOut struct {
	SandboxID string `json:"sandbox_id"`
	Status    string `json:"status"`
}

type runIn struct {
	SandboxID      string            `json:"sandbox_id" jsonschema:"the sandbox ID (must be running)"`
	Command        string            `json:"command" jsonschema:"shell command; runs as /bin/bash -l -c <command>"`
	Cwd            string            `json:"cwd,omitempty" jsonschema:"working directory (default the user's home)"`
	Env            map[string]string `json:"env,omitempty" jsonschema:"extra environment variables"`
	User           string            `json:"user,omitempty" jsonschema:"user to run as (default the sandbox's default user; root is allowed)"`
	TimeoutSeconds int               `json:"timeout_seconds,omitempty" jsonschema:"stop the command after this many seconds (default 60)"`
}

type runOut struct {
	Stdout     string `json:"stdout"`
	Stderr     string `json:"stderr"`
	ExitCode   int    `json:"exit_code" jsonschema:"the command's exit status; -1 when it was killed or timed out"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Truncated  bool   `json:"truncated,omitempty" jsonschema:"output was longer than 256 KiB and only the end is shown"`
	DurationMs int64  `json:"duration_ms"`
}

type startIn struct {
	SandboxID string            `json:"sandbox_id" jsonschema:"the sandbox ID (must be running)"`
	Command   string            `json:"command" jsonschema:"shell command; runs as /bin/bash -l -c <command>"`
	Cwd       string            `json:"cwd,omitempty" jsonschema:"working directory"`
	Env       map[string]string `json:"env,omitempty" jsonschema:"extra environment variables"`
	User      string            `json:"user,omitempty" jsonschema:"user to run as"`
}

type startOut struct {
	SandboxID string `json:"sandbox_id"`
	PID       uint32 `json:"pid"`
}

type readProcIn struct {
	SandboxID    string `json:"sandbox_id" jsonschema:"the sandbox ID"`
	PID          uint32 `json:"pid" jsonschema:"pid from start_process"`
	StdoutOffset int64  `json:"stdout_offset,omitempty" jsonschema:"read stdout from this byte offset (the stdout_offset of the previous call)"`
	StderrOffset int64  `json:"stderr_offset,omitempty" jsonschema:"read stderr from this byte offset"`
	WaitSeconds  int    `json:"wait_seconds,omitempty" jsonschema:"wait up to this long for new output or exit (default 0)"`
	MaxBytes     int    `json:"max_bytes,omitempty" jsonschema:"at most this many bytes per stream (default 65536)"`
}

type readProcOut struct {
	PID          uint32 `json:"pid"`
	Command      string `json:"command"`
	Running      bool   `json:"running"`
	ExitCode     *int   `json:"exit_code,omitempty"`
	Error        string `json:"error,omitempty"`
	Stdout       string `json:"stdout"`
	Stderr       string `json:"stderr"`
	StdoutOffset int64  `json:"stdout_offset" jsonschema:"pass this back to read only newer stdout"`
	StderrOffset int64  `json:"stderr_offset" jsonschema:"pass this back to read only newer stderr"`
	MoreOutput   bool   `json:"more_output,omitempty" jsonschema:"more output is buffered than max_bytes; call again"`
	DroppedBytes int64  `json:"dropped_bytes,omitempty" jsonschema:"output older than the 4 MiB buffer that is gone"`
}

type killProcIn struct {
	SandboxID string `json:"sandbox_id" jsonschema:"the sandbox ID"`
	PID       uint32 `json:"pid" jsonschema:"process ID"`
	Signal    string `json:"signal,omitempty" jsonschema:"SIGTERM (default) or SIGKILL"`
}

type readFileIn struct {
	SandboxID string `json:"sandbox_id" jsonschema:"the sandbox ID (must be running)"`
	Path      string `json:"path" jsonschema:"absolute path, or relative to the user's home"`
	Encoding  string `json:"encoding,omitempty" jsonschema:"text (default; falls back to base64 for binary data) or base64"`
	MaxBytes  int64  `json:"max_bytes,omitempty" jsonschema:"refuse files larger than this (default 1048576)"`
	User      string `json:"user,omitempty" jsonschema:"read as this user"`
}

type readFileOut struct {
	Path     string `json:"path"`
	Size     int    `json:"size"`
	Encoding string `json:"encoding" jsonschema:"text or base64"`
	Content  string `json:"content"`
}

type writeFileIn struct {
	SandboxID string `json:"sandbox_id" jsonschema:"the sandbox ID (must be running)"`
	Path      string `json:"path" jsonschema:"absolute path, or relative to the user's home"`
	Content   string `json:"content" jsonschema:"file content"`
	Encoding  string `json:"encoding,omitempty" jsonschema:"text (default) or base64"`
	User      string `json:"user,omitempty" jsonschema:"write as this user (owner of the file)"`
}

type writeFileOut struct {
	Path  string `json:"path"`
	Bytes int    `json:"bytes"`
}

type listFilesIn struct {
	SandboxID string `json:"sandbox_id" jsonschema:"the sandbox ID (must be running)"`
	Path      string `json:"path" jsonschema:"directory to list"`
	Depth     uint32 `json:"depth,omitempty" jsonschema:"how many levels deep (default 1)"`
	User      string `json:"user,omitempty" jsonschema:"list as this user"`
}

type fileEntry struct {
	Name          string `json:"name"`
	Path          string `json:"path"`
	Type          string `json:"type" jsonschema:"file, dir, symlink or unknown"`
	Size          int64  `json:"size"`
	Permissions   string `json:"permissions,omitempty"`
	Owner         string `json:"owner,omitempty"`
	ModifiedTime  string `json:"modified_time,omitempty"`
	SymlinkTarget string `json:"symlink_target,omitempty"`
}

type listFilesOut struct {
	Path    string      `json:"path"`
	Entries []fileEntry `json:"entries"`
}

type screenshotIn struct {
	SandboxID string `json:"sandbox_id" jsonschema:"the sandbox ID (desktop template, running)"`
	Display   string `json:"display,omitempty" jsonschema:"X display (default :0, what the e2b-desktop SDK uses)"`
}

type logsIn struct {
	SandboxID string `json:"sandbox_id" jsonschema:"the sandbox ID, from create_sandbox or list_sandboxes"`
	Limit     int    `json:"limit,omitempty" jsonschema:"how many lines, 1 to 1000 (default 100, the newest ones)"`
	Level     string `json:"level,omitempty" jsonschema:"minimum level: debug, info (default), warn or error"`
	Search    string `json:"search,omitempty" jsonschema:"only lines containing this text (case-sensitive)"`
}

type logLine struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	Stream    string `json:"stream,omitempty" jsonschema:"stdout or stderr when the line came from a process"`
}

type logsOut struct {
	SandboxID string    `json:"sandbox_id"`
	Lines     []logLine `json:"lines" jsonschema:"oldest first"`
	Truncated bool      `json:"truncated" jsonschema:"true when older lines were left out by the limit"`
}

type metricsOut struct {
	SandboxID  string  `json:"sandbox_id"`
	TakenAt    string  `json:"taken_at"`
	CPUCount   int     `json:"cpu_count"`
	CPUUsedPct float64 `json:"cpu_used_pct"`
	MemUsed    int64   `json:"mem_used_bytes"`
	MemTotal   int64   `json:"mem_total_bytes"`
	DiskUsed   int64   `json:"disk_used_bytes"`
	DiskTotal  int64   `json:"disk_total_bytes"`
	Samples    int     `json:"samples" jsonschema:"how many samples the gateway has kept for this sandbox"`
}

type usageIn struct {
	Period       string `json:"period,omitempty" jsonschema:"billing period: current (default) or YYYY-MM"`
	IncludeDaily bool   `json:"include_daily,omitempty" jsonschema:"also return the per-day breakdown"`
}

// ---- handlers ----

// getLogs reads the newest lines, then hands them back oldest first, the
// order a reader expects.
func (s *server) getLogs(ctx context.Context, _ *mcp.CallToolRequest, in logsIn) (*mcp.CallToolResult, logsOut, error) {
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	entries, err := s.cfg.API.LogsBackward(ctx, in.SandboxID, api.LogsParams{Limit: limit, Level: in.Level, Search: in.Search})
	if err != nil {
		return nil, logsOut{}, err
	}
	out := logsOut{SandboxID: in.SandboxID, Lines: make([]logLine, 0, len(entries)), Truncated: len(entries) == limit}
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		out.Lines = append(out.Lines, logLine{Timestamp: e.Timestamp.UTC().Format(time.RFC3339Nano),
			Level: e.Level, Message: e.Message, Stream: e.Fields["stream"]})
	}
	return nil, out, nil
}

func (s *server) getMetrics(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, metricsOut, error) {
	samples, err := s.cfg.API.Metrics(ctx, in.SandboxID, 0, 0)
	if err != nil {
		return nil, metricsOut{}, err
	}
	out := metricsOut{SandboxID: in.SandboxID, Samples: len(samples)}
	if len(samples) == 0 {
		return nil, out, nil
	}
	m := samples[len(samples)-1]
	out.TakenAt = m.Timestamp.UTC().Format(time.RFC3339)
	out.CPUCount, out.CPUUsedPct = m.CPUCount, m.CPUUsedPct
	out.MemUsed, out.MemTotal = m.MemUsed, m.MemTotal
	out.DiskUsed, out.DiskTotal = m.DiskUsed, m.DiskTotal
	return nil, out, nil
}

func (s *server) getUsage(ctx context.Context, _ *mcp.CallToolRequest, in usageIn) (*mcp.CallToolResult, map[string]any, error) {
	u, err := s.cfg.API.Usage(ctx, in.Period)
	if err != nil {
		return nil, nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(u.Raw, &out); err != nil {
		return nil, nil, err
	}
	if !in.IncludeDaily {
		delete(out, "daily")
	}
	return nil, out, nil
}

func (s *server) whoami(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, map[string]any, error) {
	if s.cfg.Whoami == nil {
		return nil, map[string]any{}, nil
	}
	w, err := s.cfg.Whoami(ctx)
	if err != nil {
		return nil, nil, err
	}
	w["read_only"] = s.cfg.ReadOnly
	return nil, w, nil
}

func timeStr(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}

// endStr hides the far-future end time the gateway reports for sandboxes
// without a time limit.
func endStr(t time.Time) *string {
	if t.IsZero() || t.Year() >= 9000 {
		return nil
	}
	s := timeStr(t)
	return &s
}

func (s *server) listSandboxes(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, listOut, error) {
	p := api.ListParams{Metadata: in.Metadata, Limit: in.Limit}
	switch in.State {
	case "", "all":
		p.States = []string{"running", "paused"}
	case "running", "paused":
		p.States = []string{in.State}
	default:
		return nil, listOut{}, fmt.Errorf("state must be running, paused or all")
	}
	if p.Limit == 0 {
		p.Limit = 100
	}
	sbs, next, err := s.cfg.API.ListSandboxes(ctx, p)
	if err != nil {
		return nil, listOut{}, err
	}
	out := listOut{Sandboxes: []sandboxOut{}, More: next != ""}
	for _, sb := range sbs {
		out.Sandboxes = append(out.Sandboxes, sandboxOut{SandboxID: sb.SandboxID, TemplateID: sb.TemplateID, Alias: sb.Alias, State: sb.State,
			StartedAt: timeStr(sb.StartedAt), EndAt: endStr(sb.EndAt), CPUCount: sb.CPUCount, MemoryMB: sb.MemoryMB, DiskSizeMB: sb.DiskSizeMB,
			EnvdVersion: sb.EnvdVersion, Metadata: sb.Metadata})
	}
	return nil, out, nil
}

func detailOut(d *api.SandboxDetail) sandboxOut {
	return sandboxOut{SandboxID: d.SandboxID, TemplateID: d.TemplateID, Alias: d.Alias, State: d.State, StartedAt: timeStr(d.StartedAt),
		EndAt: endStr(d.EndAt), CPUCount: d.CPUCount, MemoryMB: d.MemoryMB, DiskSizeMB: d.DiskSizeMB,
		EnvdVersion: d.EnvdVersion, Metadata: d.Metadata}
}

func (s *server) getSandbox(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, sandboxOut, error) {
	d, err := s.cfg.API.GetSandbox(ctx, in.SandboxID)
	if err != nil {
		return nil, sandboxOut{}, err
	}
	return nil, detailOut(d), nil
}

var viewChannels = map[string]string{"desktop": "desktop", "dom": "dom", "terminal": "pty"}

func (s *server) liveView(ctx context.Context, _ *mcp.CallToolRequest, in viewIn) (*mcp.CallToolResult, viewOut, error) {
	mode := in.Mode
	if mode == "" {
		mode = "desktop"
	}
	ch, ok := viewChannels[mode]
	if !ok {
		return nil, viewOut{}, errors.New("mode must be desktop, dom or terminal")
	}
	scope := "view"
	if in.Control {
		if s.cfg.ReadOnly {
			return nil, viewOut{}, errors.New("this session is read-only: only view links (control=false)")
		}
		scope = "control"
	}
	if mode == "terminal" && scope != "control" {
		return nil, viewOut{}, errors.New("terminal mode opens a shell and needs control=true")
	}
	ttl := in.TTLSeconds
	if ttl == 0 {
		ttl = 600
	}
	tok, err := s.cfg.API.MintViewerToken(ctx, in.SandboxID, api.ViewerTokenRequest{Scope: scope, Channels: []string{ch}, TTLSeconds: ttl})
	if err != nil {
		return nil, viewOut{}, err
	}
	return nil, viewOut{URL: tok.EmbedURL, ExpiresAt: tok.ExpiresAt, Scope: scope, Channel: ch}, nil
}

func (s *server) preview(ctx context.Context, _ *mcp.CallToolRequest, in previewIn) (*mcp.CallToolResult, previewOut, error) {
	l, err := s.cfg.API.MintPreviewLink(ctx, in.SandboxID, in.Port, in.TTLSeconds)
	if err != nil {
		return nil, previewOut{}, err
	}
	return nil, previewOut{URL: l.URL, Port: l.Port, ExpiresAt: l.ExpiresAt}, nil
}

func (s *server) createSandbox(ctx context.Context, _ *mcp.CallToolRequest, in createIn) (*mcp.CallToolResult, createOut, error) {
	req := api.NewSandboxV2{TemplateID: in.Template, Timeout: in.TimeoutSeconds, AllowInternetAccess: in.Internet, Metadata: in.Metadata, EnvVars: in.EnvVars}
	if req.TemplateID == "" {
		req.TemplateID = "base"
	}
	if in.AutoPause {
		req.AutoPause = &api.AutoPause{On: true}
	}
	if in.IdlePauseMinutes != nil {
		if *in.IdlePauseMinutes < 0 || *in.IdlePauseMinutes > api.MaxIdlePauseMinutes {
			return nil, createOut{}, fmt.Errorf("idle_pause_minutes must be 0 (off) to %d", api.MaxIdlePauseMinutes)
		}
		if req.AutoPause == nil {
			req.AutoPause = &api.AutoPause{On: true}
		}
		req.AutoPause.IdleMinutes = in.IdlePauseMinutes
	}
	sb, err := s.cfg.API.CreateSandbox(ctx, req)
	if err != nil {
		return nil, createOut{}, err
	}
	return nil, createOut{SandboxID: sb.SandboxID, TemplateID: sb.TemplateID, EnvdVersion: sb.EnvdVersion}, nil
}

func (s *server) killSandbox(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, statusOut, error) {
	if err := s.cfg.API.KillSandbox(ctx, in.SandboxID); err != nil {
		return nil, statusOut{}, err
	}
	s.procs.forget(in.SandboxID)
	return nil, statusOut{SandboxID: in.SandboxID, Status: "killed"}, nil
}

func (s *server) pauseSandbox(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, statusOut, error) {
	already, err := s.cfg.API.PauseSandbox(ctx, in.SandboxID, true)
	if err != nil {
		return nil, statusOut{}, err
	}
	st := "paused"
	if already {
		st = "already_paused"
	}
	return nil, statusOut{SandboxID: in.SandboxID, Status: st}, nil
}

func (s *server) resumeSandbox(ctx context.Context, _ *mcp.CallToolRequest, in resumeIn) (*mcp.CallToolResult, statusOut, error) {
	t := int32(300)
	if in.TimeoutSeconds != nil {
		t = *in.TimeoutSeconds
	}
	_, resumed, err := s.cfg.API.ConnectSandbox(ctx, in.SandboxID, t)
	if err != nil {
		return nil, statusOut{}, err
	}
	st := "already_running"
	if resumed {
		st = "resumed"
	}
	return nil, statusOut{SandboxID: in.SandboxID, Status: st}, nil
}

func (s *server) extendSandbox(ctx context.Context, _ *mcp.CallToolRequest, in extendIn) (*mcp.CallToolResult, sandboxOut, error) {
	if in.TimeoutSeconds == 0 || in.TimeoutSeconds < -1 {
		return nil, sandboxOut{}, errors.New("timeout_seconds must be positive, or -1 for no time limit")
	}
	if err := s.cfg.API.SetTimeout(ctx, in.SandboxID, in.TimeoutSeconds); err != nil {
		return nil, sandboxOut{}, err
	}
	d, err := s.cfg.API.GetSandbox(ctx, in.SandboxID)
	if err != nil {
		return nil, sandboxOut{}, err
	}
	return nil, detailOut(d), nil
}

// envdFor looks the sandbox up through the gateway (which checks access)
// and returns an envd client with the token the gateway issued.
func (s *server) envdFor(ctx context.Context, id, user string) (*envd.Client, error) {
	d, err := s.cfg.API.GetSandbox(ctx, id)
	if err != nil {
		return nil, err
	}
	if d.State == "paused" {
		return nil, fmt.Errorf("sandbox %s is paused; call resume_sandbox first", id)
	}
	if d.EnvdAccessToken == "" {
		// The gateway withholds the envd token from read-only callers.
		return nil, fmt.Errorf("this credential may not run commands or touch files in sandbox %s (it needs a role or API key with the sandboxes:use scope)", id)
	}
	return s.cfg.Envd(id, d.EnvdAccessToken, d.EnvdVersion, d.Domain, user), nil
}

const maxCommandOutput = 256 << 10

// tail keeps the last max bytes.
type tail struct {
	b         []byte
	truncated bool
}

func (t *tail) write(p []byte) {
	t.b = append(t.b, p...)
	if len(t.b) > maxCommandOutput {
		t.b = append([]byte(nil), t.b[len(t.b)-maxCommandOutput:]...)
		t.truncated = true
	}
}

func startRequest(command, cwd string, env map[string]string) envd.StartRequest {
	no := false
	req := envd.StartRequest{Process: envd.ProcessConfig{Cmd: "/bin/bash", Args: []string{"-l", "-c", command}, Envs: env}, Stdin: &no}
	if cwd != "" {
		req.Process.Cwd = &cwd
	}
	return req
}

func (s *server) runCommand(ctx context.Context, _ *mcp.CallToolRequest, in runIn) (*mcp.CallToolResult, runOut, error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, runOut{}, errors.New("command is empty")
	}
	ec, err := s.envdFor(ctx, in.SandboxID, in.User)
	if err != nil {
		return nil, runOut{}, err
	}
	timeout := time.Duration(in.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	res, err := execute(ctx, ec, startRequest(in.Command, in.Cwd, in.Env), timeout)
	return nil, res, err
}

// execute runs a command to completion (or timeout) and collects output.
func execute(ctx context.Context, ec *envd.Client, req envd.StartRequest, timeout time.Duration) (runOut, error) {
	started := time.Now()
	sctx, cancel := context.WithTimeout(ctx, timeout+5*time.Second)
	defer cancel()
	stream, err := ec.Start(sctx, req, timeout)
	if err != nil {
		return runOut{}, fmt.Errorf("start command: %w", err)
	}
	defer stream.Close()
	var stdout, stderr tail
	var pid uint32
	out := runOut{ExitCode: -1}
	for {
		ev, err := stream.Next()
		if err != nil {
			out.Stdout, out.Stderr = string(stdout.b), string(stderr.b)
			out.Truncated = stdout.truncated || stderr.truncated
			out.DurationMs = time.Since(started).Milliseconds()
			var ee *envd.Error
			if time.Since(started) >= timeout || sctx.Err() != nil || (errors.As(err, &ee) && ee.Code == "deadline_exceeded") {
				out.TimedOut = true
				if pid != 0 {
					kctx, kcancel := context.WithTimeout(context.Background(), 10*time.Second)
					_ = ec.SendSignal(kctx, pid, envd.SignalKill)
					kcancel()
				}
				return out, nil
			}
			if ctx.Err() != nil {
				return out, ctx.Err()
			}
			return out, fmt.Errorf("command stream: %w", err)
		}
		switch {
		case ev.Start != nil:
			pid = ev.Start.PID
		case ev.Data != nil:
			stdout.write(ev.Data.Stdout)
			stdout.write(ev.Data.Pty)
			stderr.write(ev.Data.Stderr)
		case ev.End != nil:
			out.Stdout, out.Stderr = string(stdout.b), string(stderr.b)
			out.Truncated = stdout.truncated || stderr.truncated
			out.DurationMs = time.Since(started).Milliseconds()
			out.ExitCode = int(ev.End.ExitCode)
			if !ev.End.Exited && out.ExitCode == 0 {
				out.ExitCode = -1
			}
			return out, nil
		}
	}
}

func (s *server) startProcess(ctx context.Context, _ *mcp.CallToolRequest, in startIn) (*mcp.CallToolResult, startOut, error) {
	if strings.TrimSpace(in.Command) == "" {
		return nil, startOut{}, errors.New("command is empty")
	}
	ec, err := s.envdFor(ctx, in.SandboxID, in.User)
	if err != nil {
		return nil, startOut{}, err
	}
	p, err := s.procs.start(ctx, ec, in.SandboxID, startRequest(in.Command, in.Cwd, in.Env), in.Command)
	if err != nil {
		return nil, startOut{}, err
	}
	return nil, startOut{SandboxID: in.SandboxID, PID: p.pid}, nil
}

func (s *server) readProcess(ctx context.Context, _ *mcp.CallToolRequest, in readProcIn) (*mcp.CallToolResult, readProcOut, error) {
	p := s.procs.get(in.SandboxID, in.PID)
	if p == nil {
		return nil, readProcOut{}, fmt.Errorf("no process %d in sandbox %s was started by this MCP server (output is only captured for start_process)", in.PID, in.SandboxID)
	}
	max := in.MaxBytes
	if max <= 0 {
		max = 64 << 10
	}
	deadline := time.Now().Add(time.Duration(in.WaitSeconds) * time.Second)
	for {
		p.mu.Lock()
		newOutput := p.stdout.end() > in.StdoutOffset || p.stderr.end() > in.StderrOffset
		if newOutput || !p.running || !time.Now().Before(deadline) {
			so, sn, sd := p.stdout.from(in.StdoutOffset, max)
			eo, en, ed := p.stderr.from(in.StderrOffset, max)
			out := readProcOut{PID: p.pid, Command: p.command, Running: p.running, ExitCode: p.exitCode, Error: p.err,
				Stdout: string(so), Stderr: string(eo), StdoutOffset: sn, StderrOffset: en,
				MoreOutput: sn < p.stdout.end() || en < p.stderr.end(), DroppedBytes: sd + ed}
			p.mu.Unlock()
			return nil, out, nil
		}
		ch := p.changed
		p.mu.Unlock()
		t := time.NewTimer(time.Until(deadline))
		select {
		case <-ch:
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return nil, readProcOut{}, ctx.Err()
		}
		t.Stop()
	}
}

func (s *server) killProcess(ctx context.Context, _ *mcp.CallToolRequest, in killProcIn) (*mcp.CallToolResult, statusOut, error) {
	sig := envd.SignalTerm
	switch in.Signal {
	case "", "SIGTERM":
	case "SIGKILL":
		sig = envd.SignalKill
	default:
		return nil, statusOut{}, errors.New("signal must be SIGTERM or SIGKILL")
	}
	ec, err := s.envdFor(ctx, in.SandboxID, "")
	if err != nil {
		return nil, statusOut{}, err
	}
	if err := ec.SendSignal(ctx, in.PID, sig); err != nil {
		// A PID envd no longer has - because it already exited, or was
		// never one of this sandbox's processes - is kill_process's success
		// case (bug #8), mirroring pause_sandbox's already_paused handling
		// above: the caller asked for the process to be gone, and it is.
		var ee *envd.Error
		if errors.As(err, &ee) && ee.Code == "not_found" {
			return nil, statusOut{SandboxID: in.SandboxID, Status: "already_exited"}, nil
		}
		return nil, statusOut{}, err
	}
	return nil, statusOut{SandboxID: in.SandboxID, Status: "signalled " + strings.TrimPrefix(sig, "SIGNAL_")}, nil
}

func (s *server) readFile(ctx context.Context, _ *mcp.CallToolRequest, in readFileIn) (*mcp.CallToolResult, readFileOut, error) {
	ec, err := s.envdFor(ctx, in.SandboxID, in.User)
	if err != nil {
		return nil, readFileOut{}, err
	}
	max := in.MaxBytes
	if max <= 0 {
		max = 1 << 20
	}
	data, err := ec.ReadFile(ctx, in.Path, max)
	if err != nil {
		return nil, readFileOut{}, err
	}
	out := readFileOut{Path: in.Path, Size: len(data)}
	if in.Encoding != "base64" && utf8.Valid(data) {
		out.Encoding, out.Content = "text", string(data)
	} else {
		out.Encoding, out.Content = "base64", base64.StdEncoding.EncodeToString(data)
	}
	return nil, out, nil
}

func (s *server) writeFile(ctx context.Context, _ *mcp.CallToolRequest, in writeFileIn) (*mcp.CallToolResult, writeFileOut, error) {
	data := []byte(in.Content)
	switch in.Encoding {
	case "", "text":
	case "base64":
		var err error
		if data, err = base64.StdEncoding.DecodeString(in.Content); err != nil {
			return nil, writeFileOut{}, fmt.Errorf("content is not valid base64: %w", err)
		}
	default:
		return nil, writeFileOut{}, errors.New("encoding must be text or base64")
	}
	ec, err := s.envdFor(ctx, in.SandboxID, in.User)
	if err != nil {
		return nil, writeFileOut{}, err
	}
	if err := ec.WriteFile(ctx, in.Path, data); err != nil {
		return nil, writeFileOut{}, err
	}
	return nil, writeFileOut{Path: in.Path, Bytes: len(data)}, nil
}

var fileTypes = map[string]string{"FILE_TYPE_FILE": "file", "FILE_TYPE_DIRECTORY": "dir", "FILE_TYPE_SYMLINK": "symlink"}

func (s *server) listFiles(ctx context.Context, _ *mcp.CallToolRequest, in listFilesIn) (*mcp.CallToolResult, listFilesOut, error) {
	ec, err := s.envdFor(ctx, in.SandboxID, in.User)
	if err != nil {
		return nil, listFilesOut{}, err
	}
	entries, err := ec.ListDir(ctx, in.Path, in.Depth)
	if err != nil {
		return nil, listFilesOut{}, err
	}
	out := listFilesOut{Path: in.Path, Entries: []fileEntry{}}
	for _, e := range entries {
		t := fileTypes[e.Type]
		if t == "" {
			t = "unknown"
		}
		fe := fileEntry{Name: e.Name, Path: e.Path, Type: t, Size: int64(e.Size), Permissions: e.Permissions, Owner: e.Owner, ModifiedTime: e.ModifiedTime}
		if e.SymlinkTarget != nil {
			fe.SymlinkTarget = *e.SymlinkTarget
		}
		out.Entries = append(out.Entries, fe)
	}
	return nil, out, nil
}

var displayRE = func(d string) bool {
	if len(d) < 2 || d[0] != ':' {
		return false
	}
	for _, c := range d[1:] {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}

func (s *server) screenshot(ctx context.Context, _ *mcp.CallToolRequest, in screenshotIn) (*mcp.CallToolResult, any, error) {
	display := in.Display
	if display == "" {
		display = ":0"
	}
	if !displayRE(display) {
		return nil, nil, errors.New("display must look like :0")
	}
	ec, err := s.envdFor(ctx, in.SandboxID, "")
	if err != nil {
		return nil, nil, err
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	file := "/tmp/anona-mcp-screenshot-" + hex.EncodeToString(rnd[:]) + ".png"
	cmd := fmt.Sprintf(`export DISPLAY=%s
command -v scrot >/dev/null || { echo "scrot is not installed: screenshots need the desktop template" >&2; exit 3; }
xdpyinfo >/dev/null 2>&1 || { echo "no X server on display $DISPLAY: start the desktop first (e2b-desktop SDK, or Xvfb $DISPLAY)" >&2; exit 4; }
scrot -o %s`, display, file)
	res, err := execute(ctx, ec, startRequest(cmd, "", nil), 30*time.Second)
	if err != nil {
		return nil, nil, err
	}
	if res.ExitCode != 0 {
		msg := strings.TrimSpace(res.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("scrot exited with %d", res.ExitCode)
		}
		return nil, nil, errors.New(msg)
	}
	png, err := ec.ReadFile(ctx, file, 32<<20)
	_, _ = execute(context.Background(), ec, startRequest("rm -f "+file, "", nil), 10*time.Second)
	if err != nil {
		return nil, nil, err
	}
	if len(png) < 8 || string(png[1:4]) != "PNG" {
		return nil, nil, errors.New("the screenshot is not a PNG file")
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: png, MIMEType: "image/png"},
		&mcp.TextContent{Text: fmt.Sprintf("Screenshot of %s display %s (%d bytes PNG)", in.SandboxID, display, len(png))},
	}}, nil, nil
}

func (s *server) ssh(ctx context.Context, _ *mcp.CallToolRequest, in idIn) (*mcp.CallToolResult, SSHConfig, error) {
	c, err := s.cfg.SSH(ctx, in.SandboxID)
	if err != nil {
		return nil, SSHConfig{}, err
	}
	return nil, *c, nil
}
