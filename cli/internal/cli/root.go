// Package cli implements the anona-compute command tree.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/auth"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/config"
)

// Exit codes. They are documented in README.md and must not change meaning.
const (
	ExitOK       = 0
	ExitError    = 1
	ExitUsage    = 2
	ExitAuth     = 3
	ExitNotFound = 4
	ExitQuota    = 5
	// ExitCLIFailed is what exec and run exit with when the CLI itself
	// failed, whatever the reason: those commands pass the remote command's
	// exit status through, so 1-5 would be ambiguous there. 125 is docker
	// run's code for the same case.
	ExitCLIFailed = 125
)

// annotationExit marks commands that pass a remote exit status through.
const annotationExit = "anona-compute/exit-codes"

// exitCodesHelp is the exit-code table, in the root help and exec's.
const exitCodesHelp = `Exit codes:
  0    success
  1    general error
  2    usage error (bad flag, argument or value)
  3    not signed in, or the credential was refused (HTTP 401/403)
  4    not found (HTTP 404)
  5    plan limit or rate limit (HTTP 429, or error_code plan_limit_exceeded)
  exec and run pass the remote command's exit status through unchanged
  (0-255; 130 after Ctrl-C) and exit 125 when anona-compute itself failed,
  for any of the reasons 1-5 above; their --json error keeps that detailed
  code in "cli_code".`

// exitError carries an explicit exit code. remote marks a status that came
// from the sandbox (or stands for one, such as 130 after Ctrl-C), which exec
// and run pass through unchanged.
type exitError struct {
	code   int
	err    error
	remote bool
}

func (e *exitError) Error() string {
	if e.err == nil {
		return ""
	}
	return e.err.Error()
}
func (e *exitError) Unwrap() error { return e.err }

func usageErr(format string, args ...any) error {
	return &exitError{code: ExitUsage, err: fmt.Errorf(format, args...)}
}

// silentExit exits with a code and prints nothing more.
func silentExit(code int) error { return &exitError{code: code} }

// remoteExit exits with a remote command's status and prints nothing more.
func remoteExit(code int) error { return &exitError{code: code, remote: true} }

type app struct {
	version string
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
	getenv  func(string) string

	// global flags
	profile    string
	apiURL     string
	apiKey     string
	output     string
	jsonFlag   bool
	verbose    bool
	reqTimeout time.Duration
	webURL     string // login --web-url

	// set in PersistentPreRunE
	ctx context.Context

	// tokens is the browser-login token source, set by client() for
	// profiles signed in with `anona-compute login`.
	tokens *auth.TokenSource
}

func (a *app) json() bool { return a.jsonFlag || a.output == "json" }

// emit writes one JSON object per line on stdout, with snake_case keys
// (jsonout.go).
func (a *app) emit(v any) { writeJSONLine(a.stdout, v) }

// emitList writes a list command's --json output: one JSON array on one
// line, `[]` when there is nothing to list. List commands used to emit one
// object per item, so an empty list printed nothing at all, which no JSON
// parser accepts; an array has a valid empty form and the same shape at
// every length.
func emitList[T any](a *app, items []T) {
	if items == nil {
		items = []T{}
	}
	a.emit(items)
}

func writeJSONLine(w io.Writer, v any) {
	b, err := snakeJSON(v)
	if err != nil {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(v)
		return
	}
	_, _ = w.Write(b)
}

// printf writes human output on stdout (text mode only).
func (a *app) printf(format string, args ...any) {
	if !a.json() {
		fmt.Fprintf(a.stdout, format, args...)
	}
}

// logf writes progress and notes on stderr (text mode only, so JSON
// consumers get clean streams).
func (a *app) logf(format string, args ...any) {
	if !a.json() {
		fmt.Fprintf(a.stderr, format, args...)
	}
}

func (a *app) verbosef(format string, args ...any) {
	if a.verbose {
		fmt.Fprintf(a.stderr, "anona-compute: "+format+"\n", args...)
	}
}

func (a *app) configPath() (string, error) { return config.Path() }

func (a *app) settings() (config.Settings, error) {
	path, err := a.configPath()
	if err != nil {
		return config.Settings{}, err
	}
	f, err := config.Load(path)
	if err != nil {
		return config.Settings{}, err
	}
	return config.Resolve(f, config.Flags{Profile: a.profile, APIURL: a.apiURL, APIKey: a.apiKey, WebURL: a.webURL}, a.getenv), nil
}

func (a *app) userAgent() string { return "anona-compute/" + a.version }

// credStore is where browser-login sessions are kept: the OS keychain when
// it works, else credentials.json (0600) next to config.toml.
// ANONA_COMPUTE_CREDENTIAL_STORE=file|keyring forces one.
func (a *app) credStore() (*auth.Store, error) {
	path, err := a.configPath()
	if err != nil {
		return nil, err
	}
	return &auth.Store{FilePath: filepath.Join(filepath.Dir(path), "credentials.json"), Mode: a.getenv("ANONA_COMPUTE_CREDENTIAL_STORE")}, nil
}

func (a *app) dashboard(s config.Settings) *auth.Client {
	return &auth.Client{WebURL: s.WebURL, UserAgent: a.sessionUserAgent(), HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// sessionUserAgent names the machine, so the dashboard's session row says
// which computer the CLI session belongs to.
func (a *app) sessionUserAgent() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown host"
	}
	return fmt.Sprintf("%s (CLI on %s; %s/%s)", a.userAgent(), host, runtime.GOOS, runtime.GOARCH)
}

// sessionTokens builds the token source of a browser-login profile.
func (a *app) sessionTokens(s config.Settings) (*auth.TokenSource, error) {
	st, err := a.credStore()
	if err != nil {
		return nil, err
	}
	secret, err := st.Load(s.CredentialStore, s.Profile)
	if err != nil {
		return nil, &exitError{code: ExitAuth, err: err}
	}
	return &auth.TokenSource{Client: a.dashboard(s), Session: secret, Org: s.Org}, nil
}

// apiKeyRE is the format any key must have to be sent (what the gateway's
// apikey.Format and the E2B SDK accept). Keys seeded for development can be
// any length.
var apiKeyRE = regexp.MustCompile(`^(ac|e2b)_[0-9a-f]+$`)

// savedKeyRE is the format of every key the gateway issues: apikey.Generate
// is "ac_" + hex of 20 random bytes, for the dashboard and the admin tool
// alike. Saving a key (login --with-api-key, config set api_key) requires it,
// so a truncated paste fails here instead of on the next command.
var savedKeyRE = regexp.MustCompile(`^(ac|e2b)_[0-9a-f]{40}$`)

const savedKeyFormatMsg = "API key has the wrong format: keys are ac_ (or legacy e2b_) followed by 40 lowercase hex characters " +
	"(a development key of another length can still be used through ANONA_COMPUTE_API_KEY or --api-key)"

// client returns an API client, or an auth error when no key is set.
func (a *app) client() (*api.Client, config.Settings, error) {
	s, err := a.settings()
	if err != nil {
		return nil, s, err
	}
	hc := &http.Client{}
	if a.reqTimeout > 0 {
		hc.Timeout = a.reqTimeout
	}
	// An API key (flag, environment or profile) wins over a browser login.
	if s.APIKey == "" && s.Session {
		ts, err := a.sessionTokens(s)
		if err != nil {
			return nil, s, err
		}
		a.tokens = ts
		a.verbosef("api %s, profile %s, session via %s (org %s)", s.APIURL, s.Profile, s.WebURL, s.Org)
		return &api.Client{BaseURL: s.APIURL, Token: func(ctx context.Context) (string, error) {
			t, err := ts.Token(ctx)
			if errors.Is(err, auth.ErrSessionRevoked) {
				return "", &exitError{code: ExitAuth, err: err}
			}
			return t, err
		}, Refresh: ts.Invalidate, UserAgent: a.userAgent(), HTTP: hc}, s, nil
	}
	if s.APIKey == "" {
		return nil, s, &exitError{code: ExitAuth, err: errors.New("not signed in: run `anona-compute login` (browser), or set ANONA_COMPUTE_API_KEY or E2B_API_KEY, pass --api-key, or run `anona-compute login --with-api-key`")}
	}
	if !apiKeyRE.MatchString(s.APIKey) {
		return nil, s, &exitError{code: ExitAuth, err: errors.New("API key has the wrong format: keys are ac_ (or legacy e2b_) followed by lowercase hex")}
	}
	a.verbosef("api %s, profile %s", s.APIURL, s.Profile)
	return &api.Client{BaseURL: s.APIURL, APIKey: s.APIKey, UserAgent: a.userAgent(), HTTP: hc}, s, nil
}

func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   BinaryName,
		Short: "Anona Compute CLI: create and use sandboxes",
		Long: `anona-compute manages Anona Compute sandboxes through the E2B-compatible API.

Configuration comes from flags, then the environment (ANONA_COMPUTE_API_KEY or
E2B_API_KEY, ANONA_COMPUTE_DOMAIN or E2B_DOMAIN, ANONA_COMPUTE_API_URL or
E2B_API_URL), then the profile in $XDG_CONFIG_HOME/anona-compute/config.toml.
anona-compute doctor checks the whole chain.

JSON output:
  --json (or -o json) prints JSON on stdout. A list command (list, ps,
  template list, config list) prints one array, [] when there is nothing to
  list; other commands print one object per line. Every key is
  snake_case (sandbox_id, template_id, cpu_count, api_url), as in the MCP
  server's tools; keys inside metadata, envs and log fields are yours and are
  printed as given. An error is one object on stderr:
    {"error":{"message":"...","exit_code":N,"status":404,
              "error_code":"...","request_id":"..."}}
  status, error_code and request_id appear when known; a failure with no
  HTTP answer has error_code "network_error" or "timeout".

` + exitCodesHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if a.output != "text" && a.output != "json" {
				return usageErr("--output must be text or json, not %q", a.output)
			}
			return nil
		},
	}
	root.SetIn(a.stdin)
	root.SetOut(a.stdout)
	root.SetErr(a.stderr)
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return &exitError{code: ExitUsage, err: fmt.Errorf("%w\nRun '%s --help' for usage.", err, c.CommandPath())}
	})
	root.CompletionOptions.DisableDefaultCmd = true
	// --version prints exactly what `anona-compute version` prints (Main
	// handles it, so --json applies too).
	root.Version = a.version
	root.SetVersionTemplate("{{.Version}}\n")
	root.Flags().Bool("version", false, "print the CLI version (same as the version command)")

	pf := root.PersistentFlags()
	pf.StringVar(&a.profile, "profile", "", "config profile to use (env ANONA_COMPUTE_PROFILE; default \"default\")")
	pf.StringVar(&a.apiURL, "api-url", "", "API URL (env ANONA_COMPUTE_API_URL, E2B_API_URL; default https://api.<E2B_DOMAIN>)")
	pf.StringVar(&a.apiKey, "api-key", "", "API key (env ANONA_COMPUTE_API_KEY, E2B_API_KEY)")
	pf.StringVarP(&a.output, "output", "o", "text", "output format: text or json")
	pf.BoolVar(&a.jsonFlag, "json", false, "JSON output on stdout (same as -o json)")
	pf.BoolVarP(&a.verbose, "verbose", "v", false, "verbose output on stderr")
	pf.DurationVar(&a.reqTimeout, "request-timeout", 0, "timeout for each API request (0 = none)")

	root.AddCommand(
		newVersionCmd(a),
		newDoctorCmd(a),
		newConfigCmd(a),
		newLoginCmd(a),
		newLogoutCmd(a),
		newWhoamiCmd(a),
		newSandboxCmd(a),
		newMCPCmd(a),
		newCreateCmd(a),
		newListCmd(a),
		newInfoCmd(a),
		newKillCmd(a),
		newPauseCmd(a),
		newResumeCmd(a),
		newTimeoutCmd(a),
		newRenameCmd(a),
		newIdlePauseCmd(a),
		newForkCmd(a),
		newExecCmd(a),
		newRunCmd(a),
		newOutputCmd(a),
		newPsCmd(a),
		newLogsCmd(a),
		newConnectCmd(a),
		newSSHCmd(a),
		newSCPCmd(a),
		newViewCmd(a),
		newDesktopCmd(a),
		newTemplateCmd(a),
		newVaultCmd(a),
		newUsageCmd(a),
		newPreviewCmd(a),
	)
	for _, nb := range notBuilt {
		root.AddCommand(newNotBuiltCmd(nb))
	}
	return root
}

// noArgs rejects positional arguments with a usage error (also catches
// unknown subcommands on commands that have children).
func noArgs(cmd *cobra.Command, args []string) error {
	if len(args) > 0 {
		if cmd.HasSubCommands() {
			return usageErr("unknown command %q for %q\nRun '%s --help' for usage.", args[0], cmd.CommandPath(), cmd.CommandPath())
		}
		return usageErr("%s takes no arguments", cmd.CommandPath())
	}
	return nil
}

func exactArgs(n int, names string) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) != n {
			return usageErr("usage: %s %s", cmd.CommandPath(), names)
		}
		return nil
	}
}

// Main runs the CLI and returns the process exit code.
func Main(version string, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{version: version, stdin: stdin, stdout: stdout, stderr: stderr, getenv: os.Getenv}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.ctx = ctx
	root := newRoot(a)
	root.SetArgs(args)
	if rootVersionFlag(args) {
		if jsonRequested(args) {
			a.jsonFlag = true
			a.emit(map[string]string{"version": a.version})
		} else {
			fmt.Fprintln(stdout, a.version)
		}
		return ExitOK
	}
	cmd, err := root.ExecuteContextC(ctx)
	if err == nil {
		return ExitOK
	}
	code := exitCodeFor(err)
	cliCode := code
	var ee *exitError
	isRemote := errors.As(err, &ee) && ee.remote
	cliFailure := cmd != nil && cmd.Annotations[annotationExit] == "passthrough" && !isRemote
	if cliFailure {
		code = ExitCLIFailed
	}
	if errors.As(err, &ee) && ee.err == nil {
		return code
	}
	if a.json() {
		obj := errorObject(err, code)
		if cliFailure {
			obj["cli_code"] = cliCode
		}
		writeJSONLine(stderr, map[string]any{"error": obj})
	} else {
		msg := err.Error()
		if id := requestID(err); id != "" {
			msg += " (request " + id + ")"
		}
		fmt.Fprintf(stderr, "anona-compute: %s\n", msg)
	}
	return code
}

// rootVersionFlag reports whether args are the root's --version: only
// global flags before it, no subcommand (a subcommand's own --version, if
// any, is the subcommand's business).
func rootVersionFlag(args []string) bool {
	seen := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return false
		case arg == "--version":
			seen = true
		case arg == "-o" || arg == "--output" || arg == "--profile" || arg == "--api-url" || arg == "--api-key" || arg == "--request-timeout":
			i++ // the flag's value
		case !strings.HasPrefix(arg, "-"):
			return false
		}
	}
	return seen
}

func jsonRequested(args []string) bool {
	for i, arg := range args {
		if arg == "--json" || arg == "--json=true" || arg == "-o=json" || arg == "--output=json" || arg == "-ojson" {
			return true
		}
		if (arg == "-o" || arg == "--output") && i+1 < len(args) && args[i+1] == "json" {
			return true
		}
	}
	return false
}

// errorObject is the body of the --json error envelope. Every key is
// snake_case; status, error_code and request_id are present only when known,
// and outcome_unknown only when true.
func errorObject(err error, code int) map[string]any {
	obj := map[string]any{"message": err.Error(), "exit_code": code}
	if st := api.StatusOf(err); st != 0 {
		obj["status"] = st
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) && apiErr.ErrorCode != "" {
		obj["error_code"] = apiErr.ErrorCode
	} else if c := networkErrorCode(err); c != "" {
		obj["error_code"] = c
	}
	if apiErr != nil && apiErr.OutcomeUnknown {
		obj["outcome_unknown"] = true
	}
	if id := requestID(err); id != "" {
		obj["request_id"] = id
	}
	return obj
}

// requestID is the gateway's X-Request-Id for a failed request, if any.
func requestID(err error) string {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return apiErr.RequestID
	}
	return ""
}

// networkErrorCode classifies a failure that never got an HTTP answer.
func networkErrorCode(err error) string {
	if api.StatusOf(err) != 0 {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return "timeout"
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "timeout"
	}
	var ue *url.Error
	var oe *net.OpError
	var de *net.DNSError
	if errors.As(err, &ue) || errors.As(err, &oe) || errors.As(err, &de) {
		return "network_error"
	}
	return ""
}

// BinaryName is the executable's name: `anona` is another product's CLI
// (the Anona Memory CLI), so this one is anona-compute.
const BinaryName = "anona-compute"

func exitCodeFor(err error) int {
	var ee *exitError
	if errors.As(err, &ee) {
		return ee.code
	}
	switch api.StatusOf(err) {
	case 0:
	case http.StatusUnauthorized, http.StatusForbidden:
		// A plan limit is a quota whatever its status (403 for keys,
		// templates, paused sandboxes and sizes), not a sign-in problem.
		if api.CodeOf(err) == "plan_limit_exceeded" {
			return ExitQuota
		}
		return ExitAuth
	case http.StatusNotFound:
		return ExitNotFound
	case http.StatusTooManyRequests:
		return ExitQuota
	default:
		return ExitError
	}
	// Errors cobra produces before our handlers run.
	if s := err.Error(); strings.HasPrefix(s, "unknown command") || strings.HasPrefix(s, "unknown flag") || strings.HasPrefix(s, "unknown shorthand flag") {
		return ExitUsage
	}
	return ExitError
}
