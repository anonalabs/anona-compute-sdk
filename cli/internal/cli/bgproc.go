package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/envd"
)

// Background commands (exec -b) outlive the CLI, and envd keeps no output
// for a process nobody is attached to: Process/Connect only streams what is
// written after it connects. (The MCP server's read_process_output works
// because the server itself stays up and buffers the stream; a CLI that
// exits cannot.) So exec -b has the command write its stdout, stderr and
// exit status to files in the sandbox, named after the shell's PID, which is
// the PID envd reports and exec -b prints:
//
//	/tmp/anona-compute-bg/<pid>.out, <pid>.err, <pid>.exit
//
// `anona-compute output ID PID` reads them (-f follows until the process
// ends) and `anona-compute ps ID` lists envd's running processes.

const defaultBgDir = "/tmp/anona-compute-bg"

// bgDir is where background output is kept. ANONA_COMPUTE_TEST_BG_DIR moves
// it for the tests, whose fake sandbox runs commands on the test machine.
func (a *app) bgDir() string {
	if d := a.getenv("ANONA_COMPUTE_TEST_BG_DIR"); d != "" {
		return d
	}
	return defaultBgDir
}

// bgStartWait bounds how long output waits for a just-started background
// command to create its output file before reporting it as unknown.
const bgStartWait = 5 * time.Second

// bgMarker starts every wrapped command, so ps can tell them apart.
const bgMarker = "# anona-compute background\n"

// backgroundCommand wraps command so that its output and exit status land in
// the sandbox. The directory is world-writable with the sticky bit, as /tmp
// is, so commands run as different users can all use it. The EXIT trap also
// keeps bash from exec'ing the last command directly, which would skip it.
func (a *app) backgroundCommand(command string) string {
	d := shellQuote(a.bgDir())
	return bgMarker +
		"mkdir -p -m 1777 " + d + " 2>/dev/null\n" +
		"exec >" + d + "/$$.out 2>" + d + "/$$.err </dev/null\n" +
		"trap 'echo $? >" + d + "/$$.exit' EXIT\n" +
		command
}

// userCommand undoes backgroundCommand for display.
func userCommand(command string) (string, bool) {
	if !strings.HasPrefix(command, bgMarker) {
		return command, false
	}
	lines := strings.SplitN(command, "\n", 5)
	if len(lines) < 5 {
		return command, false
	}
	return lines[4], true
}

// sandboxEnvd resolves a running sandbox's envd client.
func (a *app) sandboxEnvd(id, user string) (*envd.Client, error) {
	cl, s, err := a.client()
	if err != nil {
		return nil, err
	}
	d, err := cl.GetSandbox(a.ctx, id)
	if err != nil {
		return nil, err
	}
	if d.State == "paused" {
		return nil, fmt.Errorf("sandbox %s is paused; run `anona-compute resume %s` first", id, id)
	}
	return a.envdClient(s, id, d.EnvdAccessToken, d.EnvdVersion, d.Domain, user), nil
}

// capture runs a short script and returns its stdout, stderr and exit code.
func capture(ctx context.Context, ec *envd.Client, script string) (stdout, stderr []byte, code int, err error) {
	no := false
	stream, err := ec.Start(ctx, envd.StartRequest{
		Process: envd.ProcessConfig{Cmd: "/bin/bash", Args: []string{"-l", "-c", script}},
		Stdin:   &no,
	}, time.Minute)
	if err != nil {
		return nil, nil, 0, err
	}
	defer stream.Close()
	for {
		ev, err := stream.Next()
		if err != nil {
			return stdout, stderr, 0, fmt.Errorf("command stream: %w", err)
		}
		switch {
		case ev.Data != nil:
			stdout = append(stdout, ev.Data.Stdout...)
			stderr = append(stderr, ev.Data.Stderr...)
		case ev.End != nil:
			return stdout, stderr, int(ev.End.ExitCode), nil
		}
	}
}

// bgState is what is known about a background process.
type bgState struct {
	State    string // running, exited, ended (no exit status recorded), missing
	ExitCode *int
}

func (a *app) bgStatus(ctx context.Context, ec *envd.Client, pid uint32) (bgState, error) {
	d := shellQuote(a.bgDir())
	p := strconv.FormatUint(uint64(pid), 10)
	// "starting": the process exists but its wrapper has not yet redirected
	// its output (exec -b prints the PID as soon as the process starts, a few
	// milliseconds before the wrapper's first line creates the .out file).
	script := fmt.Sprintf(`if [ ! -e %[1]s/%[2]s.out ]; then if [ -d /proc/%[2]s ]; then echo starting; else echo missing; fi
elif [ -s %[1]s/%[2]s.exit ]; then echo "exited $(cat %[1]s/%[2]s.exit)"
elif [ -d /proc/%[2]s ]; then echo running
else echo ended; fi`, d, p)
	out, errOut, code, err := capture(ctx, ec, script)
	if err != nil {
		return bgState{}, err
	}
	if code != 0 {
		return bgState{}, fmt.Errorf("read process status: exit %d: %s", code, strings.TrimSpace(string(errOut)))
	}
	f := strings.Fields(string(out))
	if len(f) == 0 {
		return bgState{}, fmt.Errorf("read process status: no answer")
	}
	st := bgState{State: f[0]}
	if st.State == "exited" && len(f) > 1 {
		if n, err := strconv.Atoi(f[1]); err == nil {
			st.ExitCode = &n
		}
	}
	return st, nil
}

func (st bgState) describe(pid uint32) string {
	switch st.State {
	case "running":
		return fmt.Sprintf("process %d is still running", pid)
	case "exited":
		if st.ExitCode != nil {
			return fmt.Sprintf("process %d exited with code %d", pid, *st.ExitCode)
		}
	}
	return fmt.Sprintf("process %d has ended (its exit status was not recorded)", pid)
}

func (st bgState) json(id string, pid uint32) map[string]any {
	m := map[string]any{"sandbox_id": id, "pid": pid, "state": st.State}
	if st.ExitCode != nil {
		m["exit_code"] = *st.ExitCode
	}
	return m
}

func parsePID(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n == 0 {
		return 0, usageErr("PID must be a positive number, not %q", s)
	}
	return uint32(n), nil
}

func newOutputCmd(a *app) *cobra.Command {
	var follow bool
	var user string
	c := &cobra.Command{
		Use:   "output SANDBOX_ID PID",
		Short: "Print the output of a command started with exec --background",
		Long: `Print the stdout and stderr of a command started with anona-compute exec -b,
which keeps them in the sandbox under /tmp/anona-compute-bg. stdout goes to
stdout and stderr to stderr; a line on stderr says whether the process is
still running or how it exited.

  anona-compute exec -b ID -- 'npm run dev'      prints the PID
  anona-compute output ID PID                    everything so far
  anona-compute output ID PID -f                 follow until the process ends

With --json: one object {"pid","state","exit_code"?,"stdout","stderr"}; with
-f, {"type":"stdout"|"stderr","data"} lines and a final {"type":"exit",...}.
Processes started any other way have no saved output (anona-compute ps lists them).`,
		Args: exactArgs(2, "SANDBOX_ID PID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			pid, err := parsePID(args[1])
			if err != nil {
				return err
			}
			ec, err := a.sandboxEnvd(id, user)
			if err != nil {
				return err
			}
			st, err := a.bgStatus(a.ctx, ec, pid)
			if err != nil {
				return err
			}
			// A command started a moment ago may not have created its output
			// file yet; give it a few seconds before calling it unknown.
			for deadline := time.Now().Add(bgStartWait); st.State == "starting" && time.Now().Before(deadline); {
				select {
				case <-a.ctx.Done():
					return a.ctx.Err()
				case <-time.After(100 * time.Millisecond):
				}
				if st, err = a.bgStatus(a.ctx, ec, pid); err != nil {
					return err
				}
			}
			if st.State == "missing" || st.State == "starting" {
				return &exitError{code: ExitNotFound, err: fmt.Errorf("no saved output for process %d in %s: only commands started with `anona-compute exec -b` keep their output (see `anona-compute ps %s`)", pid, id, id)}
			}
			d := shellQuote(a.bgDir())
			p := strconv.FormatUint(uint64(pid), 10)
			if follow {
				// tail --pid stops once the process has ended and its output is read.
				script := fmt.Sprintf("trap 'kill $(jobs -p) 2>/dev/null' TERM\ntail -c +1 -f --pid=%[2]s %[1]s/%[2]s.out & tail -c +1 -f --pid=%[2]s %[1]s/%[2]s.err >&2 & wait", d, p)
				if _, err := a.runRemote(ec, script, &execOpts{noExitEvent: true}); err != nil {
					return err
				}
				sctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				if st, err = a.bgStatus(sctx, ec, pid); err != nil {
					return err
				}
				if a.json() {
					m := st.json(id, pid)
					m["type"] = "exit"
					a.emit(m)
				} else {
					a.logf("anona-compute: %s\n", st.describe(pid))
				}
				return nil
			}
			out, _, _, err := capture(a.ctx, ec, fmt.Sprintf("cat %s/%s.out", d, p))
			if err != nil {
				return err
			}
			errOut, _, _, err := capture(a.ctx, ec, fmt.Sprintf("cat %s/%s.err", d, p))
			if err != nil {
				return err
			}
			if a.json() {
				m := st.json(id, pid)
				m["stdout"], m["stderr"] = string(out), string(errOut)
				a.emit(m)
				return nil
			}
			_, _ = a.stdout.Write(out)
			_, _ = a.stderr.Write(errOut)
			a.logf("anona-compute: %s\n", st.describe(pid))
			return nil
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing output until the process ends")
	c.Flags().StringVarP(&user, "user", "u", "", "user to read the output as (default: the sandbox's default user)")
	return c
}

func newPsCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "ps SANDBOX_ID",
		Short: "List the processes running in a sandbox",
		Long: `List the processes envd started in a sandbox that are still running (exec,
exec -b, connect shells, SDK commands). OUTPUT is "saved" for commands started
with anona-compute exec -b, whose output anona-compute output ID PID prints.`,
		Args: exactArgs(1, "SANDBOX_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			ec, err := a.sandboxEnvd(id, "")
			if err != nil {
				return err
			}
			ps, err := ec.List(a.ctx)
			if err != nil {
				return fmt.Errorf("list processes: %w", err)
			}
			type row struct {
				PID        uint32 `json:"pid"`
				Command    string `json:"command"`
				Background bool   `json:"background"`
				Tag        string `json:"tag,omitempty"`
			}
			var rows []row
			for _, p := range ps {
				command := strings.Join(append([]string{p.Config.Cmd}, p.Config.Args...), " ")
				bg := false
				if p.Config.Cmd == "/bin/bash" && len(p.Config.Args) == 3 && p.Config.Args[0] == "-l" && p.Config.Args[1] == "-c" {
					command, bg = userCommand(p.Config.Args[2])
				}
				r := row{PID: p.PID, Command: command, Background: bg}
				if p.Tag != nil {
					r.Tag = *p.Tag
				}
				rows = append(rows, r)
			}
			if a.json() {
				for _, r := range rows {
					a.emit(map[string]any{"sandbox_id": id, "pid": r.PID, "command": r.Command, "background": r.Background, "tag": r.Tag})
				}
				return nil
			}
			if len(rows) == 0 {
				fmt.Fprintln(a.stderr, "No processes.")
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "PID\tOUTPUT\tCOMMAND")
			for _, r := range rows {
				saved := "-"
				if r.Background {
					saved = "saved"
				}
				line := strings.ReplaceAll(r.Command, "\n", " ")
				if len(line) > 80 {
					line = line[:77] + "..."
				}
				fmt.Fprintf(tw, "%d\t%s\t%s\n", r.PID, saved, line)
			}
			return tw.Flush()
		},
	}
	return c
}
