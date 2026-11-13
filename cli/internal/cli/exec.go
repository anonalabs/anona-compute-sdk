package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/config"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/envd"
)

type execOpts struct {
	user       string
	cwd        string
	envs       []string
	background bool
	stdin      bool
	timeout    time.Duration
	// noExitEvent leaves out the final {"type":"exit"} JSON line, for
	// callers that report the end themselves (output --follow).
	noExitEvent bool
}

// register adds the exec flags. run passes standalone=false: its -e sets
// sandbox environment variables (create) and it has no --background.
func (o *execOpts) register(c *cobra.Command, standalone bool) {
	f := c.Flags()
	f.StringVarP(&o.user, "user", "u", "", "user to run as (default: the sandbox's default user)")
	f.StringVarP(&o.cwd, "cwd", "w", "", "working directory")
	f.BoolVarP(&o.stdin, "stdin", "i", false, "send this terminal's stdin to the command (closed at EOF)")
	if standalone {
		f.StringArrayVarP(&o.envs, "env", "e", nil, "environment variable KEY=VALUE for the command (repeatable)")
		f.BoolVarP(&o.background, "background", "b", false, "start the command, print its PID and return; read its output later with anona-compute output")
	}
	f.DurationVar(&o.timeout, "cmd-timeout", 0, "stop waiting for the command after this long (0 = no limit)")
}

// stdinIgnoredWarning is printed when data may be waiting on stdin but -i
// was not given: without it the command's stdin is empty.
const stdinIgnoredWarning = "anona-compute: warning: stdin is not sent to the command without -i/--stdin; it reads an empty stdin\n"

// stdinMayHaveData reports whether r is a pipe or a regular file: a
// terminal or /dev/null (both character devices) are not worth a warning.
func stdinMayHaveData(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok || f == nil {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	m := fi.Mode()
	if m&os.ModeCharDevice != 0 {
		return false
	}
	if m.IsRegular() {
		return fi.Size() > 0
	}
	return m&os.ModeNamedPipe != 0 || m&os.ModeSocket != 0
}

// pumpStdin streams the CLI's stdin to the remote process in order (one
// SendInput at a time) and closes the remote stdin at EOF.
func (a *app) pumpStdin(ec *envd.Client, pid uint32) {
	buf := make([]byte, 64<<10)
	for {
		n, err := a.stdin.Read(buf)
		if n > 0 {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			serr := ec.SendStdin(ctx, pid, append([]byte(nil), buf[:n]...))
			cancel()
			if serr != nil {
				// The process has usually exited already.
				a.verbosef("send stdin: %v", serr)
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				a.verbosef("read stdin: %v", err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if cerr := ec.CloseStdin(ctx, pid); cerr != nil {
				a.verbosef("close stdin: %v", cerr)
			}
			cancel()
			return
		}
	}
}

var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

func shellQuote(s string) string {
	if s != "" && safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildCommand follows the E2B CLI (packages/cli/src/commands/sandbox/
// exec_helpers.ts): one argument is run as a shell string; several are
// shell-quoted and joined.
func buildCommand(parts []string) string {
	if len(parts) == 1 {
		return parts[0]
	}
	q := make([]string, len(parts))
	for i, p := range parts {
		q[i] = shellQuote(p)
	}
	return strings.Join(q, " ")
}

func (a *app) envdClient(s config.Settings, id, token, version string, domain *string, user string) *envd.Client {
	base := s.SandboxURL
	if base == "" {
		d := s.Domain
		if domain != nil && *domain != "" {
			d = *domain
		}
		base = envd.URL(id, d)
	}
	a.verbosef("envd %s", base)
	return &envd.Client{BaseURL: base, SandboxID: id, AccessToken: token, EnvdVersion: version, User: user,
		UserAgent: a.userAgent(), HTTP: &http.Client{}}
}

// runRemote starts a command through envd and streams its output. It returns
// the remote exit code. Interrupts send SIGTERM to the remote process, a
// second interrupt sends SIGKILL (process.Signal has no SIGINT).
func (a *app) runRemote(ec *envd.Client, command string, o *execOpts) (int, error) {
	envs, err := parseKV(o.envs, "--env")
	if err != nil {
		return 0, err
	}
	if o.background {
		command = a.backgroundCommand(command)
	}
	stdin := o.stdin
	if !stdin && !o.background && stdinMayHaveData(a.stdin) {
		a.logf(stdinIgnoredWarning)
	}
	req := envd.StartRequest{
		// The SDKs run every command as /bin/bash -l -c <cmd>
		// (python-sdk commands/command.py, js-sdk commands/index.ts).
		Process: envd.ProcessConfig{Cmd: "/bin/bash", Args: []string{"-l", "-c", command}, Envs: envs},
		Stdin:   &stdin,
	}
	if o.cwd != "" {
		req.Process.Cwd = &o.cwd
	}

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(sigCh)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := ec.Start(ctx, req, o.timeout)
	if err != nil {
		return 0, fmt.Errorf("start command: %w", err)
	}
	defer stream.Close()

	type result struct {
		ev  *envd.Event
		err error
	}
	events := make(chan result, 16)
	go func() {
		for {
			ev, err := stream.Next()
			events <- result{ev, err}
			if err != nil {
				return
			}
		}
	}()

	var pid uint32
	signals := 0
	for {
		select {
		case sig := <-sigCh:
			signals++
			if pid == 0 {
				return 130, &exitError{code: 130, remote: true, err: fmt.Errorf("interrupted (%v) before the command started", sig)}
			}
			name := envd.SignalTerm
			if signals > 1 {
				name = envd.SignalKill
			}
			a.logf("anona-compute: sending %s to remote process %d\n", strings.TrimPrefix(name, "SIGNAL_"), pid)
			sctx, scancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := ec.SendSignal(sctx, pid, name); err != nil {
				fmt.Fprintf(a.stderr, "anona-compute: send signal: %v\n", err)
			}
			scancel()
		case r := <-events:
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					return 0, errors.New("command stream ended without an exit status")
				}
				return 0, fmt.Errorf("command stream: %w", r.err)
			}
			ev := r.ev
			switch {
			case ev.Start != nil:
				pid = ev.Start.PID
				a.verbosef("remote pid %d", pid)
				if o.background {
					follow := fmt.Sprintf("%s output %s %d", BinaryName, ec.SandboxID, pid)
					if a.json() {
						a.emit(map[string]any{"sandbox_id": ec.SandboxID, "pid": pid, "status": "started", "output_command": follow})
					} else {
						fmt.Fprintln(a.stdout, pid)
						a.logf("anona-compute: started in the background; read its output with `%s` (add -f to follow)\n", follow)
					}
					return 0, nil
				}
				if stdin {
					go a.pumpStdin(ec, pid)
				}
			case ev.Data != nil:
				a.writeOutput("stdout", ev.Data.Stdout, a.stdout)
				a.writeOutput("stderr", ev.Data.Stderr, a.stderr)
				a.writeOutput("stdout", ev.Data.Pty, a.stdout)
			case ev.End != nil:
				code := int(ev.End.ExitCode)
				if ev.End.Error != nil && *ev.End.Error != "" {
					a.verbosef("remote error: %s", *ev.End.Error)
				}
				if code < 0 || code > 255 {
					// Killed by a signal: envd reports exited=false and no
					// usable code.
					code = 1
					if signals > 0 {
						code = 130
					}
				}
				if a.json() && !o.noExitEvent {
					a.emit(map[string]any{"type": "exit", "pid": pid, "exit_code": code, "status": ev.End.Status})
				}
				return code, nil
			}
		}
	}
}

func (a *app) writeOutput(stream string, data []byte, w io.Writer) {
	if len(data) == 0 {
		return
	}
	if a.json() {
		a.emit(map[string]any{"type": stream, "data": string(data)})
		return
	}
	_, _ = w.Write(data)
}

func newExecCmd(a *app) *cobra.Command {
	o := &execOpts{}
	c := &cobra.Command{
		Use:     "exec SANDBOX_ID -- COMMAND [ARGS...]",
		Aliases: []string{"ex"},
		Short:   "Run a command in a sandbox",
		Long: `Run a command in a running sandbox and stream its output. anona-compute exits with
the command's exit code. With one argument the command is a shell string
(anona-compute exec ID -- 'ls | wc -l'); several arguments are quoted and joined.

The command's stdin is empty unless -i/--stdin is given; with -i the CLI's
stdin is streamed to it and closed at EOF:

  tar -cz src | anona-compute exec -i ID -- 'tar -xz -C /home/user'

-b/--background starts the command and prints its PID. Its stdout and
stderr are kept in the sandbox (under /tmp/anona-compute-bg); read them with
anona-compute output ID PID (-f to follow) and list processes with
anona-compute ps ID.

With --json, output is one object per line: {"type":"stdout"|"stderr","data":...}
and finally {"type":"exit","exit_code":N}.

` + exitCodesHelp,
		Annotations: map[string]string{annotationExit: "passthrough"},
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return usageErr("usage: anona-compute exec SANDBOX_ID -- COMMAND [ARGS...]")
			}
			if o.background && o.stdin {
				return usageErr("--stdin cannot be used with --background: a background command has no stdin")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, s, err := a.client()
			if err != nil {
				return err
			}
			id := args[0]
			d, err := cl.GetSandbox(a.ctx, id)
			if err != nil {
				return err
			}
			if d.State == "paused" {
				return fmt.Errorf("sandbox %s is paused; run `anona-compute resume %s` first", id, id)
			}
			ec := a.envdClient(s, id, d.EnvdAccessToken, d.EnvdVersion, d.Domain, o.user)
			code, err := a.runRemote(ec, buildCommand(args[1:]), o)
			if err != nil {
				return err
			}
			if code != 0 {
				return remoteExit(code)
			}
			return nil
		},
	}
	o.register(c, true)
	return c
}

func newRunCmd(a *app) *cobra.Command {
	co := &createOpts{}
	eo := &execOpts{}
	var keep bool
	c := &cobra.Command{
		Use:   "run [flags] -- COMMAND [ARGS...]",
		Short: "Create a sandbox, run one command, then delete the sandbox",
		Long: `Create a sandbox, run one command in it, print a summary on stderr, and delete
the sandbox (also on error or Ctrl-C). anona-compute exits with the command's exit code;
-i/--stdin streams this terminal's stdin to the command.

Cost is printed when the API reports it; the current API does not, so the
summary says "unknown" rather than guessing.

` + exitCodesHelp,
		Annotations: map[string]string{annotationExit: "passthrough"},
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return usageErr("usage: anona-compute run [flags] -- COMMAND [ARGS...]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, s, err := a.client()
			if err != nil {
				return err
			}
			req, err := co.request(s.Template)
			if err != nil {
				return err
			}
			started := time.Now()
			sb, err := cl.CreateSandbox(a.ctx, req)
			if err != nil {
				return err
			}
			a.logf("anona-compute: created sandbox %s\n", sb.SandboxID)
			if a.json() {
				a.emit(map[string]any{"type": "created", "sandbox_id": sb.SandboxID, "template_id": sb.TemplateID})
			}
			deleted := false
			defer func() {
				if keep || deleted {
					return
				}
				kctx, kcancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer kcancel()
				if err := cl.KillSandbox(kctx, sb.SandboxID); err != nil {
					fmt.Fprintf(a.stderr, "anona-compute: delete %s: %v\n", sb.SandboxID, err)
				}
			}()

			ec := a.envdClient(s, sb.SandboxID, sb.EnvdAccessToken, sb.EnvdVersion, sb.Domain, eo.user)
			code, runErr := a.runRemote(ec, buildCommand(args), eo)

			if !keep {
				kctx, kcancel := context.WithTimeout(context.Background(), 30*time.Second)
				err := cl.KillSandbox(kctx, sb.SandboxID)
				kcancel()
				deleted = true
				if err != nil && api.StatusOf(err) != http.StatusNotFound {
					fmt.Fprintf(a.stderr, "anona-compute: delete %s: %v\n", sb.SandboxID, err)
				}
			}
			dur := time.Since(started)
			if a.json() {
				a.emit(map[string]any{"type": "summary", "sandbox_id": sb.SandboxID, "exit_code": code,
					"duration_ms": dur.Milliseconds(), "cost": nil, "deleted": !keep})
			} else {
				state := "deleted"
				if keep {
					state = "kept running"
				}
				fmt.Fprintf(a.stderr, "anona-compute: sandbox %s %s after %s; exit code %d; cost: unknown (the API does not report cost yet)\n",
					sb.SandboxID, state, dur.Round(time.Millisecond), code)
			}
			if runErr != nil {
				return runErr
			}
			if code != 0 {
				return remoteExit(code)
			}
			return nil
		},
	}
	co.register(c)
	eo.register(c, false)
	c.Flags().BoolVar(&keep, "keep", false, "do not delete the sandbox after the command")
	return c
}
