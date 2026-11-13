package cli

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
)

// logPollInterval matches the E2B CLI's follow loop
// (packages/cli/src/commands/sandbox/logs.ts waits 400 ms between polls).
var logPollInterval = 400 * time.Millisecond

func newLogsCmd(a *app) *cobra.Command {
	var follow bool
	var since, level, search string
	c := &cobra.Command{
		Use:   "logs SANDBOX_ID",
		Short: "Show sandbox logs",
		Long: `Show sandbox logs (GET /v2/sandboxes/{id}/logs), oldest first.

--follow keeps polling for new lines until the sandbox ends or you press
Ctrl-C (exit 0).`,
		Args: exactArgs(1, "SANDBOX_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch level {
			case "", "debug", "info", "warn", "error":
			default:
				return usageErr("--level must be debug, info, warn or error")
			}
			var cursor *time.Time
			if since != "" {
				d, err := time.ParseDuration(since)
				if err != nil || d <= 0 {
					return usageErr("--since must be a duration such as 10m or 1h")
				}
				t := time.Now().Add(-d)
				cursor = &t
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			id := args[0]
			// seen holds the entries printed at the cursor's millisecond, so a
			// repeated query from the same cursor prints nothing twice.
			seen := map[string]bool{}
			fetch := func() (int, error) {
				printed := 0
				for {
					entries, err := cl.Logs(a.ctx, id, api.LogsParams{Cursor: cursor, Limit: 1000, Level: level, Search: search})
					if err != nil {
						return printed, err
					}
					fresh := 0
					for _, e := range entries {
						key := e.Timestamp.Format(time.RFC3339Nano) + "\x00" + e.Level + "\x00" + e.Message
						if cursor != nil && e.Timestamp.UnixMilli() == cursor.UnixMilli() && seen[key] {
							continue
						}
						if cursor == nil || e.Timestamp.UnixMilli() != cursor.UnixMilli() {
							seen = map[string]bool{}
						}
						t := e.Timestamp
						cursor = &t
						seen[key] = true
						a.printLog(e)
						fresh++
					}
					printed += fresh
					if len(entries) < 1000 {
						return printed, nil
					}
					if fresh == 0 {
						// A full page inside one millisecond: step past it.
						t := cursor.Add(time.Millisecond)
						cursor = &t
					}
				}
			}
			if _, err := fetch(); err != nil {
				return err
			}
			if !follow {
				return nil
			}
			tick := time.NewTicker(logPollInterval)
			defer tick.Stop()
			checks := 0
			for {
				select {
				case <-a.ctx.Done():
					return nil
				case <-tick.C:
				}
				if _, err := fetch(); err != nil {
					if a.ctx.Err() != nil {
						return nil
					}
					if api.StatusOf(err) == http.StatusNotFound {
						a.logf("anona-compute: sandbox %s is gone\n", id)
						return nil
					}
					return err
				}
				// Every ~2 s, stop when the sandbox is gone or paused.
				checks++
				if checks%5 == 0 {
					d, err := cl.GetSandbox(a.ctx, id)
					if a.ctx.Err() != nil {
						return nil
					}
					if api.StatusOf(err) == http.StatusNotFound || (err == nil && d.State != "running") {
						_, _ = fetch()
						a.logf("anona-compute: sandbox %s is no longer running\n", id)
						return nil
					}
				}
			}
		},
	}
	c.Flags().BoolVarP(&follow, "follow", "f", false, "keep printing new lines")
	c.Flags().StringVar(&since, "since", "", "only lines newer than this (e.g. 10m, 1h)")
	c.Flags().StringVar(&level, "level", "", "minimum level: debug, info, warn or error")
	c.Flags().StringVar(&search, "search", "", "only lines containing this text (case-sensitive)")
	return c
}

func (a *app) printLog(e api.SandboxLogEntry) {
	if a.json() {
		a.emit(e)
		return
	}
	fmt.Fprintf(a.stdout, "%s %-5s %s\n", e.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z"), strings.ToUpper(e.Level), e.Message)
}
