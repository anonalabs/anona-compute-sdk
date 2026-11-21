package cli

import (
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
)

// runnersEnabled reports whether Anona Runners (beta) is shown: the `runners` command group in help and
// completion, and the three runners_* MCP tools. Runners is hidden until launch, so it is off unless
// ANONA_ENABLE_RUNNERS=1. Hidden is only hidden: `anona-compute runners ...` still runs when typed, and the
// gateway decides whether the routes exist (ANONA_RUNNERS). Launch by making this return true.
func (a *app) runnersEnabled() bool { return a.getenv("ANONA_ENABLE_RUNNERS") == "1" }

func newRunnersCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Hidden: !a.runnersEnabled(),
		Use:    "runners",
		Short:  "Run GitHub Actions jobs on Anona sandboxes (beta)",
		Long: `Anona Runners is in beta: behaviour and limits may change.

Run GitHub Actions jobs on Anona runners.

Install the Anona GitHub App on your organization with "runners connect", then
point a workflow at a size with runs-on: anona-small, anona-medium, anona-large
or anona-xl. Each job runs in a fresh sandbox of that size. A size above your
plan's limit is refused. Runners need a paid plan and private repositories.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(newRunnersConnectCmd(a), newRunnersStatusCmd(a), newRunnersJobsCmd(a),
		newRunnersJobCmd(a), newRunnersDisconnectCmd(a))
	return c
}

func newRunnersConnectCmd(a *app) *cobra.Command {
	var noBrowser bool
	c := &cobra.Command{
		Use:   "connect",
		Short: "Install the GitHub App and link it to this workspace",
		Long: `Start linking a GitHub installation (POST /v1/runners/installations/link).

Prints the GitHub URL and opens it in your browser. After you install the app
you land on the dashboard, which finishes linking the installation to this
workspace. The URL carries a signed state and expires after 15 minutes.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			u, err := cl.LinkRunnerInstallation(a.ctx)
			if err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]any{"url": u})
			} else {
				fmt.Fprintln(a.stdout, u)
			}
			if !noBrowser && a.getenv("ANONA_COMPUTE_NO_BROWSER") == "" {
				if err := openBrowser(u); err != nil {
					a.logf("Could not open a browser (%v); open the URL yourself.\n", err)
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&noBrowser, "no-browser", false, "print the URL but do not open a browser")
	return c
}

func newRunnersStatusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show linked installations and the runs-on labels to use",
		Long: `List linked GitHub installations (GET /v1/runners/installations), then a
runs-on snippet for each size the plan allows. A plan without runners shows the
reason instead of snippets.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			inst, err := cl.ListRunnerInstallations(a.ctx)
			if err != nil {
				return err
			}
			if a.json() {
				a.emit(inst)
				return nil
			}
			if len(inst.Installations) == 0 {
				fmt.Fprintln(a.stdout, "no installations: run \"anona-compute runners connect\"")
			} else {
				tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "ID\tACCOUNT\tTYPE\tSTATUS\tLINKED")
				for _, i := range inst.Installations {
					st := "active"
					if i.Suspended {
						st = "suspended"
					}
					fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", i.ID, i.AccountLogin, i.AccountType, st, i.LinkedAt.UTC().Format(time.RFC3339))
				}
				tw.Flush()
			}
			if !inst.Eligible {
				fmt.Fprintf(a.stdout, "\nrunners are not available: %s\n", inst.Reason)
				return nil
			}
			fmt.Fprintln(a.stdout, "\nUse in a workflow:")
			for _, s := range inst.AllowedSizes {
				fmt.Fprintf(a.stdout, "  runs-on: anona-%s\n", s)
			}
			return nil
		},
	}
}

func newRunnersJobsCmd(a *app) *cobra.Command {
	var repo, status, before string
	var limit int
	c := &cobra.Command{
		Use:   "jobs",
		Short: "List recent runner jobs",
		Long: `List recent jobs (GET /v1/runners/jobs), newest first.

--repo is owner/name. --status filters by job status. DURATION is start to
finish, or the time so far for a job still running.

When older jobs exist the output ends with a cursor; pass it as --before to
read the next page. With --json the result is {"jobs": [...], "next_cursor":
"..."}, and next_cursor is empty on the last page.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			jobs, next, err := cl.ListRunnerJobs(a.ctx, repo, status, limit, before)
			if err != nil {
				return err
			}
			if a.json() {
				if jobs == nil {
					jobs = []api.RunnerJob{}
				}
				a.emit(struct {
					Jobs       []api.RunnerJob `json:"jobs"`
					NextCursor string          `json:"nextCursor"`
				}{jobs, next})
				return nil
			}
			if len(jobs) == 0 {
				fmt.Fprintln(a.stdout, "no jobs")
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "ID\tREPO\tJOB\tSIZE\tSTATUS\tSTARTED\tDURATION")
			for _, j := range jobs {
				started := "-"
				if j.StartedAt != nil {
					started = j.StartedAt.UTC().Format(time.RFC3339)
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", j.ID, j.Repo, j.JobName, j.Size, j.Status, started, jobDuration(j))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if next != "" {
				fmt.Fprintf(a.stdout, "\nmore jobs: anona-compute runners jobs --before %s\n", next)
			}
			return nil
		},
	}
	c.Flags().StringVar(&before, "before", "", "page cursor: list jobs older than the previous page (printed when more exist)")
	c.Flags().StringVar(&repo, "repo", "", "only jobs of this repository (owner/name)")
	c.Flags().StringVar(&status, "status", "", "only jobs with this status")
	c.Flags().IntVar(&limit, "limit", 20, "how many jobs to list")
	return c
}

func jobDuration(j api.RunnerJob) string {
	if j.StartedAt == nil {
		return "-"
	}
	end := time.Now()
	if j.FinishedAt != nil {
		end = *j.FinishedAt
	}
	return end.Sub(*j.StartedAt).Round(time.Second).String()
}

func newRunnersJobCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "job <id>",
		Short: "Show one runner job",
		Long:  "Show one runner job (GET /v1/runners/jobs/{id}), including the sandbox it ran in and its GitHub URL.",
		Args:  exactArgs(1, "<id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			j, err := cl.GetRunnerJob(a.ctx, args[0])
			if err != nil {
				return err
			}
			if a.json() {
				a.emit(j)
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			row := func(k, v string) {
				if v != "" {
					fmt.Fprintf(tw, "%s:\t%s\n", k, v)
				}
			}
			ts := func(t *time.Time) string {
				if t == nil {
					return ""
				}
				return t.UTC().Format(time.RFC3339)
			}
			row("id", j.ID)
			row("repo", j.Repo)
			row("workflow", j.Workflow)
			row("job", j.JobName)
			row("label", j.Label)
			row("size", j.Size)
			row("status", j.Status)
			// A reason is a failure on refused, failed and cancelled rows, a
			// waiting note while queued or dispatching, and noise otherwise.
			switch j.Status {
			case "refused", "failed", "cancelled", "queued", "dispatching":
				row("reason", j.Reason)
			}
			row("conclusion", j.Conclusion)
			row("sandbox", j.SandboxID)
			row("queued", ts(j.QueuedAt))
			row("started", ts(j.StartedAt))
			row("finished", ts(j.FinishedAt))
			row("duration", jobDuration(*j))
			row("github", j.GithubURL)
			return tw.Flush()
		},
	}
}

func newRunnersDisconnectCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "disconnect <installation-id>",
		Short: "Unlink a GitHub installation",
		Long: `Unlink an installation from this workspace (DELETE /v1/runners/installations/{id}).

Queued jobs from it are no longer picked up. The GitHub App stays installed on
GitHub until you remove it there.`,
		Args: exactArgs(1, "<installation-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			if err := cl.DeleteRunnerInstallation(a.ctx, args[0]); err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]any{"installation_id": args[0], "disconnected": true})
				return nil
			}
			fmt.Fprintf(a.stdout, "disconnected %s\n", args[0])
			return nil
		},
	}
}
