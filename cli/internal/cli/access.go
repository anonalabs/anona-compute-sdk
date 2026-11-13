package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/envd"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/relay"
)

// sshFiles are the per-sandbox key, certificate and known_hosts paths.
type sshFiles struct {
	dir, key, cert, knownHosts string
	user                       string
	expiresAt                  string
}

// sshDir is $XDG_RUNTIME_DIR/anona-compute/ssh/<id> (falling back to the user cache dir).
func (a *app) sshDir(id string) (string, error) {
	base := a.getenv("XDG_RUNTIME_DIR")
	if base == "" {
		c, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		base = c
	}
	if strings.ContainsAny(id, "/\\") || id == "" || id == "." || id == ".." {
		return "", usageErr("bad sandbox ID %q", id)
	}
	d := filepath.Join(base, "anona-compute", "ssh", id)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	return d, os.Chmod(d, 0o700)
}

// prepareSSH makes (once) an ephemeral Ed25519 key for the sandbox and gets
// a fresh certificate for it from the gateway.
func (a *app) prepareSSH(cl *api.Client, id string) (*sshFiles, error) {
	dir, err := a.sshDir(id)
	if err != nil {
		return nil, err
	}
	f := &sshFiles{dir: dir, key: filepath.Join(dir, "id_ed25519"), knownHosts: filepath.Join(dir, "known_hosts")}
	f.cert = f.key + "-cert.pub"
	if _, err := os.Stat(f.key); err != nil {
		if _, err := exec.LookPath("ssh-keygen"); err != nil {
			return nil, errors.New("ssh-keygen not found: install OpenSSH")
		}
		out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "anona-"+id, "-f", f.key).CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("ssh-keygen: %v: %s", err, out)
		}
	}
	pub, err := os.ReadFile(f.key + ".pub")
	if err != nil {
		return nil, err
	}
	c, err := cl.SSHCertificate(a.ctx, id, strings.TrimSpace(string(pub)))
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(f.cert, []byte(c.Certificate+"\n"), 0o600); err != nil {
		return nil, err
	}
	f.user, f.expiresAt = c.User, c.ExpiresAt
	a.verbosef("ssh certificate for %s@%s until %s", c.User, id, c.ExpiresAt)
	return f, nil
}

// proxyCommand is how ssh re-invokes this binary as its ProxyCommand. Global
// flags that select the account are passed on; an API key given with
// --api-key travels in the environment (sshEnv), never on a command line.
func (a *app) proxyCommand() string {
	exe, err := os.Executable()
	if err != nil {
		exe = "anona-compute"
	}
	parts := []string{shellQuote(exe)}
	if a.profile != "" {
		parts = append(parts, "--profile", shellQuote(a.profile))
	}
	if a.apiURL != "" {
		parts = append(parts, "--api-url", shellQuote(a.apiURL))
	}
	return strings.Join(append(parts, "ssh", "--proxy", "%n"), " ")
}

func (a *app) sshEnv() []string {
	env := os.Environ()
	if a.apiKey != "" {
		env = append(env, "ANONA_COMPUTE_API_KEY="+a.apiKey)
	}
	return env
}

func (f *sshFiles) options(proxy string) []string {
	return []string{
		"-o", "ProxyCommand=" + proxy,
		"-o", "IdentityFile=" + f.key,
		"-o", "CertificateFile=" + f.cert,
		"-o", "IdentitiesOnly=yes",
		"-o", "UserKnownHostsFile=" + f.knownHosts,
		"-o", "StrictHostKeyChecking=accept-new",
	}
}

// runTool runs ssh or scp with the terminal attached and returns its exit code.
func (a *app) runTool(name string, args []string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return fmt.Errorf("%s not found: install OpenSSH", name)
	}
	a.verbosef("%s %s", name, strings.Join(args, " "))
	if a.stdin == os.Stdin && a.stdout == os.Stdout && a.stderr == os.Stderr {
		// Replace this process, so signals and the exit status are ssh's own.
		return execReplace(path, append([]string{name}, args...), a.sshEnv())
	}
	cmd := exec.Command(path, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = a.stdin, a.stdout, a.stderr
	cmd.Env = a.sshEnv()
	// ssh handles Ctrl-C itself; anona-compute just waits for it.
	signal.Ignore(os.Interrupt)
	defer signal.Reset(os.Interrupt)
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return silentExit(ee.ExitCode())
		}
		return err
	}
	return nil
}

// openPipe mints a control token for channel and opens it on the stream host.
func (a *app) openPipe(cl *api.Client, id, channel, scope, path string) (*relay.Conn, error) {
	tok, err := cl.MintViewerToken(a.ctx, id, api.ViewerTokenRequest{Scope: scope, Channels: []string{channel}, TTLSeconds: 600})
	if err != nil {
		return nil, err
	}
	u, err := tok.SocketURL(path)
	if err != nil {
		return nil, err
	}
	a.verbosef("stream %s", u)
	c, err := relay.Dial(a.ctx, u, relay.Hello{Token: tok.Token, Channel: channel, Sandbox: id}, &http.Client{})
	if err != nil {
		return nil, streamErr(err)
	}
	return c, nil
}

func streamErr(err error) error {
	var ce *relay.CloseError
	if !errors.As(err, &ce) {
		return err
	}
	switch ce.Code {
	case 4401:
		return &exitError{code: ExitAuth, err: err}
	case 4404:
		return &exitError{code: ExitNotFound, err: err}
	case 4429:
		return &exitError{code: ExitQuota, err: err}
	case 4409:
		return fmt.Errorf("%w (the sandbox is paused; run `anona-compute resume`)", err)
	}
	return err
}

func newSSHCmd(a *app) *cobra.Command {
	var proxy, printConfig bool
	c := &cobra.Command{
		Use:   "ssh SANDBOX_ID [-- SSH_ARGS...]",
		Short: "SSH into a sandbox",
		Long: `Open an OpenSSH session to a sandbox. anona-compute makes an ephemeral Ed25519 key
under $XDG_RUNTIME_DIR/anona-compute/ssh/<id>, has the gateway sign it (a 10-minute
certificate for the sandbox user), and runs the system ssh with
ProxyCommand "anona-compute ssh --proxy %n", which carries the connection over the
stream relay. Arguments after -- go to ssh (a remote command, -L, -R, ...).

--print-config prints an ssh_config block, so scp, rsync and editors can use
"Host <id>" directly; the certificate in it expires after 10 minutes, so run
it again (or anona-compute ssh) to renew.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 {
				return usageErr("usage: anona-compute ssh SANDBOX_ID [-- SSH_ARGS...]")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			id := args[0]
			if proxy {
				conn, err := a.openPipe(cl, id, "ssh", "control", "/v1/tcp")
				if err != nil {
					return err
				}
				return streamErr(conn.Pipe(a.ctx, a.stdin, a.stdout, false))
			}
			f, err := a.prepareSSH(cl, id)
			if err != nil {
				return err
			}
			if printConfig {
				if a.json() {
					a.emit(map[string]any{"host": id, "user": f.user, "proxy_command": a.proxyCommand(), "identity_file": f.key,
						"certificate_file": f.cert, "known_hosts_file": f.knownHosts, "expires_at": f.expiresAt})
					return nil
				}
				fmt.Fprintf(a.stdout, "# anona-compute sandbox %s; certificate valid until %s\nHost %s\n  User %s\n  ProxyCommand %s\n  IdentityFile %s\n  CertificateFile %s\n  IdentitiesOnly yes\n  UserKnownHostsFile %s\n  StrictHostKeyChecking accept-new\n",
					id, f.expiresAt, id, f.user, a.proxyCommand(), f.key, f.cert, f.knownHosts)
				return nil
			}
			sshArgs := append(f.options(a.proxyCommand()), f.user+"@"+id)
			return a.runTool("ssh", append(sshArgs, args[1:]...))
		},
	}
	c.Flags().BoolVar(&proxy, "proxy", false, "act as the OpenSSH ProxyCommand: pipe stdin/stdout to the sandbox sshd")
	c.Flags().BoolVar(&printConfig, "print-config", false, "print an ssh_config block for the sandbox instead of connecting")
	return c
}

func newSCPCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "scp SANDBOX_ID [SCP_FLAGS...] SOURCE... TARGET",
		Short: "Copy files to or from a sandbox over SSH",
		Long: `Copy files with the system scp through the same certificate and relay as
anona-compute ssh. Write sandbox paths with a leading colon:

  anona-compute scp sbx_123 ./data.csv :/home/user/
  anona-compute scp sbx_123 -- -r :/home/user/out ./out`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 3 {
				return usageErr("usage: anona-compute scp SANDBOX_ID SOURCE... TARGET (sandbox paths start with ':')")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			id := args[0]
			f, err := a.prepareSSH(cl, id)
			if err != nil {
				return err
			}
			rest := make([]string, 0, len(args))
			remote := false
			for _, p := range args[1:] {
				if strings.HasPrefix(p, ":") {
					p, remote = f.user+"@"+id+p, true
				}
				rest = append(rest, p)
			}
			if !remote {
				return usageErr("no sandbox path: write sandbox paths with a leading ':' (for example :/home/user/file)")
			}
			return a.runTool("scp", append(f.options(a.proxyCommand()), rest...))
		},
	}
}

// newConnectCmd is an interactive shell on an envd PTY (no sshd).
func newConnectCmd(a *app) *cobra.Command {
	var user, cwd string
	c := &cobra.Command{
		Use:   "connect SANDBOX_ID",
		Short: "Open an interactive shell in a sandbox",
		Long: `Open an interactive login shell (/bin/bash -i -l) on a PTY through envd, the
same channel the E2B SDK's sandbox.pty uses; no sshd is needed. The local
terminal is put in raw mode and resizes follow it. Type Ctrl-] then d to
detach (the shell keeps running). anona-compute exits with the shell's exit code.`,
		Args: exactArgs(1, "SANDBOX_ID"),
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
			ec := a.envdClient(s, id, d.EnvdAccessToken, d.EnvdVersion, d.Domain, user)
			code, err := a.interactive(ec, cwd)
			if err != nil {
				return err
			}
			if code != 0 {
				return silentExit(code)
			}
			return nil
		},
	}
	c.Flags().StringVarP(&user, "user", "u", "", "user to run the shell as (default: the sandbox's default user)")
	c.Flags().StringVarP(&cwd, "cwd", "w", "", "working directory")
	return c
}

func (a *app) termSize() envd.PTYSize {
	if f, ok := a.stdout.(*os.File); ok {
		if w, h, err := term.GetSize(int(f.Fd())); err == nil && w > 0 && h > 0 {
			return envd.PTYSize{Cols: uint32(w), Rows: uint32(h)}
		}
	}
	return envd.PTYSize{Cols: 80, Rows: 24}
}

func (a *app) interactive(ec *envd.Client, cwd string) (int, error) {
	cfg := envd.ProcessConfig{Cmd: "/bin/bash", Args: []string{"-i", "-l"},
		Envs: map[string]string{"TERM": first(a.getenv("TERM"), "xterm-256color"), "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8"}}
	if cwd != "" {
		cfg.Cwd = &cwd
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := ec.StartPTY(ctx, cfg, a.termSize())
	if err != nil {
		return 0, fmt.Errorf("start shell: %w", err)
	}
	defer stream.Close()
	ev, err := stream.Next()
	if err != nil {
		return 0, fmt.Errorf("start shell: %w", err)
	}
	if ev.Start == nil {
		return 0, errors.New("envd did not report the shell's pid")
	}
	pid := ev.Start.PID

	if f, ok := a.stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		old, err := term.MakeRaw(int(f.Fd()))
		if err == nil {
			defer func() { _ = term.Restore(int(f.Fd()), old) }()
		}
	}
	winch := make(chan os.Signal, 1)
	notifyWinch(winch)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			rctx, rcancel := context.WithTimeout(ctx, 5*time.Second)
			_ = ec.ResizePTY(rctx, pid, a.termSize())
			rcancel()
		}
	}()

	detached := make(chan struct{})
	go func() {
		buf := make([]byte, 4096)
		escape := false
		for {
			n, err := a.stdin.Read(buf)
			if n > 0 {
				data := buf[:n]
				// Ctrl-] d detaches (the escape byte is 0x1d).
				out := make([]byte, 0, n)
				for _, b := range data {
					switch {
					case escape && (b == 'd' || b == 'D'):
						close(detached)
						return
					case escape:
						escape = false
						out = append(out, 0x1d, b)
					case b == 0x1d:
						escape = true
					default:
						out = append(out, b)
					}
				}
				if len(out) > 0 {
					sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
					serr := ec.SendPTYInput(sctx, pid, out)
					scancel()
					if serr != nil {
						return
					}
				}
			}
			if err != nil {
				// stdin closed: send EOF to the shell (Ctrl-D), as a terminal would.
				sctx, scancel := context.WithTimeout(ctx, 10*time.Second)
				_ = ec.SendPTYInput(sctx, pid, []byte{0x04})
				scancel()
				return
			}
		}
	}()

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
	for {
		select {
		case <-detached:
			fmt.Fprintf(a.stderr, "\r\nanona: detached; shell pid %d keeps running\r\n", pid)
			return 0, nil
		case r := <-events:
			if r.err != nil {
				if errors.Is(r.err, io.EOF) {
					return 0, errors.New("shell stream ended without an exit status")
				}
				return 0, r.err
			}
			switch {
			case r.ev.Data != nil:
				_, _ = a.stdout.Write(r.ev.Data.Pty)
			case r.ev.End != nil:
				code := int(r.ev.End.ExitCode)
				if code < 0 || code > 255 {
					code = 1
				}
				return code, nil
			}
		}
	}
}

func first(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

var viewModes = map[string]string{"dom": "dom", "terminal": "pty", "desktop": "desktop"}

// desktopTemplates are the platform templates with a desktop (X display,
// VNC and a browser): the only ones a desktop or DOM live view can show.
var desktopTemplates = map[string]bool{"desktop": true, "agent-desktop": true}

// noDesktopTemplates are the platform templates known to have none.
var noDesktopTemplates = map[string]bool{"base": true, "python": true, "node": true, "code-interpreter": true, "coding-agent": true}

// checkDesktop refuses (exit 2) a desktop or DOM view of a sandbox whose
// template is a platform template without a desktop: the gateway would mint
// a link that opens onto nothing. A custom template may have been built from
// the desktop image, so it only gets a warning. --force skips the check.
func (a *app) checkDesktop(cl *api.Client, id, what string, force bool) error {
	if force {
		return nil
	}
	d, err := cl.GetSandbox(a.ctx, id)
	if err != nil {
		return err
	}
	tmpl := first(d.Alias, d.TemplateID)
	if desktopTemplates[d.TemplateID] || desktopTemplates[d.Alias] {
		return nil
	}
	if noDesktopTemplates[d.TemplateID] || noDesktopTemplates[d.Alias] {
		return usageErr("sandbox %s runs the %s template, which has no desktop, so a %s would show nothing; "+
			"create one with -t desktop or -t agent-desktop (or pass --force)", id, tmpl, what)
	}
	fmt.Fprintf(a.stderr, "anona-compute: warning: sandbox %s runs template %s; a %s needs a desktop (the desktop and agent-desktop templates have one)\n", id, tmpl, what)
	return nil
}

// Live-view token lifetimes (gateway stream/token: MinTTL, MaxTTL).
const (
	minViewTTL = 30 * time.Second
	maxViewTTL = 10 * time.Minute
)

func newViewCmd(a *app) *cobra.Command {
	var mode string
	var control, force bool
	var ttl time.Duration
	c := &cobra.Command{
		Use:   "view SANDBOX_ID",
		Short: "Open a live view of a sandbox",
		Long: `Mint a live-view token and print the embed URL (the token is in the URL
fragment, which browsers never send to a server). Modes: dom (browser
agents), terminal (a new shell; needs --control) and desktop.`,
		Args: exactArgs(1, "SANDBOX_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch, ok := viewModes[mode]
			if !ok {
				return usageErr("--mode must be dom, terminal or desktop")
			}
			if ttl < minViewTTL || ttl > maxViewTTL {
				return usageErr("--ttl must be 30s to 10m for a live view, not %s", ttl)
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			if mode == "desktop" || mode == "dom" {
				if err := a.checkDesktop(cl, args[0], mode+" view", force); err != nil {
					return err
				}
			}
			scope := "view"
			if control {
				scope = "control"
			}
			tok, err := cl.MintViewerToken(a.ctx, args[0], api.ViewerTokenRequest{Scope: scope, Channels: []string{ch}, TTLSeconds: int(ttl / time.Second)})
			if err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]any{"embed_url": tok.EmbedURL, "expires_at": tok.ExpiresAt, "scope": scope, "channel": ch})
				return nil
			}
			fmt.Fprintln(a.stdout, tok.EmbedURL)
			a.logf("anona-compute: %s %s link valid until %s\n", scope, mode, tok.ExpiresAt)
			return nil
		},
	}
	c.Flags().StringVar(&mode, "mode", "dom", "dom, terminal or desktop")
	c.Flags().BoolVar(&control, "control", false, "allow input (control scope)")
	c.Flags().DurationVar(&ttl, "ttl", 10*time.Minute, "token lifetime, 30s to 10m")
	c.Flags().BoolVar(&force, "force", false, "mint a desktop or dom view even if the sandbox's template has no desktop")
	return c
}

func newDesktopCmd(a *app) *cobra.Command {
	var listen, mode string
	var viewOnly, force bool
	c := &cobra.Command{
		Use:   "desktop SANDBOX_ID",
		Short: "Open a sandbox desktop",
		Long: `Reach the desktop of a sandbox (desktop template).

  anona-compute desktop ID                        print the live-view URL (control)
  anona-compute desktop ID --vnc 127.0.0.1:5901   serve the desktop as plain VNC (RFB) on a
                                          local port for any VNC client; each client
                                          gets its own session through the relay
  anona-compute desktop ID --mode novnc           print the E2B-compatible noVNC URL on port
                                          6080 (started by the e2b-desktop SDK)`,
		Args: exactArgs(1, "SANDBOX_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if mode != "live" && mode != "novnc" {
				return usageErr("--mode must be live or novnc")
			}
			cl, s, err := a.client()
			if err != nil {
				return err
			}
			if err := a.checkDesktop(cl, id, "desktop", force); err != nil {
				return err
			}
			switch {
			case mode == "novnc":
				u := fmt.Sprintf("https://6080-%s.%s/vnc.html?autoconnect=true&resize=scale", id, s.Domain)
				if a.json() {
					a.emit(map[string]string{"url": u})
				} else {
					fmt.Fprintln(a.stdout, u)
				}
				return nil
			case mode != "live":
				return usageErr("--mode must be live or novnc")
			case listen == "":
				tok, err := cl.MintViewerToken(a.ctx, id, api.ViewerTokenRequest{Scope: "control", Channels: []string{"desktop"}, TTLSeconds: 600})
				if err != nil {
					return err
				}
				if a.json() {
					a.emit(map[string]string{"embed_url": tok.EmbedURL, "expires_at": tok.ExpiresAt})
				} else {
					fmt.Fprintln(a.stdout, tok.EmbedURL)
				}
				return nil
			}
			scope := "control"
			if viewOnly {
				scope = "view"
			}
			return a.serveVNC(cl, id, listen, scope)
		},
	}
	c.Flags().StringVar(&listen, "vnc", "", "serve the desktop as VNC on this local address (for example 127.0.0.1:5901)")
	c.Flags().StringVar(&mode, "mode", "live", "live (Anona relay) or novnc (E2B noVNC URL)")
	c.Flags().BoolVar(&viewOnly, "view-only", false, "with --vnc: view scope (the sandbox serves a view-only VNC server)")
	c.Flags().BoolVar(&force, "force", false, "go ahead even if the sandbox's template has no desktop")
	return c
}

// serveVNC bridges local RFB clients to the sandbox's desktop channel.
func (a *app) serveVNC(cl *api.Client, id, listen, scope string) error {
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		return err
	}
	defer ln.Close()
	if a.json() {
		a.emit(map[string]string{"type": "listening", "addr": ln.Addr().String()})
	} else {
		fmt.Fprintf(a.stderr, "anona-compute: desktop of %s on vnc://%s (Ctrl-C to stop)\n", id, ln.Addr())
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)
	go func() {
		<-stop
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			return nil // listener closed by Ctrl-C
		}
		go func() {
			defer conn.Close()
			rc, err := a.openPipe(cl, id, "desktop", scope, "/v1/ws")
			if err != nil {
				fmt.Fprintf(a.stderr, "anona-compute: desktop: %v\n", err)
				return
			}
			if err := rc.Pipe(context.Background(), conn, conn, true); err != nil {
				a.verbosef("desktop session: %v", err)
			}
		}()
	}
}
