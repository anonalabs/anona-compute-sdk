package cli

import (
	"strconv"
	"time"

	"github.com/spf13/cobra"
)

func newPreviewCmd(a *app) *cobra.Command {
	var ttl time.Duration
	var revoke, public, private bool
	c := &cobra.Command{
		Use:   "preview SANDBOX_ID [PORT]",
		Short: "Share a sandbox port with a signed preview link",
		Long: `Print a shareable link to one port of a sandbox, for example a dev server on 3000.

Anyone with the link can open that port in a browser until it expires (--ttl,
default 1h, at most 24h): the sandbox edge exchanges it for a cookie scoped to
that port's host. Other ports and envd stay private.

  anona-compute preview <id> 3000
  anona-compute preview <id> 3000 --ttl 15m
  anona-compute preview <id> --revoke          end every link (and live-view token) of the sandbox
  anona-compute preview <id> 3000 --public     serve the port to anyone, no link needed
  anona-compute preview <id> 3000 --private    back to links only`,
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 1 || len(args) > 2 || (len(args) == 1 && !revoke) {
				return usageErr("usage: anona-compute preview SANDBOX_ID PORT [--ttl D | --public | --private], or SANDBOX_ID --revoke")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			id := args[0]
			if !revoke {
				port, err := strconv.Atoi(args[1])
				if err != nil || port < 1 || port > 65535 {
					return usageErr("PORT must be 1 to 65535")
				}
				// The gateway's limits (handlers/access MinTTL, sbxedge
				// MaxPreviewTTL); 0 is its default, 1h.
				if ttl != 0 && (ttl < time.Minute || ttl > 24*time.Hour) {
					return usageErr("--ttl must be 1m to 24h for a preview link, not %s", ttl)
				}
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			if revoke {
				if err := cl.RevokePreviewLinks(a.ctx, id); err != nil {
					return err
				}
				if a.json() {
					a.emit(map[string]any{"sandbox_id": id, "status": "revoked"})
				}
				a.printf("revoked every preview link and live-view token of %s\n", id)
				return nil
			}
			port, _ := strconv.Atoi(args[1]) // checked above
			if public || private {
				if public && private {
					return usageErr("--public and --private exclude each other")
				}
				s, err := cl.SetPortPublic(a.ctx, id, port, public)
				if err != nil {
					return err
				}
				if a.json() {
					a.emit(s)
					return nil
				}
				state := "private (links only)"
				if public {
					state = "public (anyone with the URL)"
				}
				a.printf("port %d of %s is now %s; public ports: %v\n", port, id, state, s.PublicPorts)
				return nil
			}
			l, err := cl.MintPreviewLink(a.ctx, id, port, int(ttl.Seconds()))
			if err != nil {
				return err
			}
			if a.json() {
				a.emit(l)
				return nil
			}
			a.printf("%s\n", l.URL)
			a.logf("port %d of %s, valid until %s\n", port, id, l.ExpiresAt)
			return nil
		},
	}
	c.Flags().DurationVar(&ttl, "ttl", 0, "link lifetime, 1m to 24h (default 1h)")
	c.Flags().BoolVar(&revoke, "revoke", false, "revoke every preview link and live-view token of the sandbox")
	c.Flags().BoolVar(&public, "public", false, "make the port public instead of printing a link")
	c.Flags().BoolVar(&private, "private", false, "make the port private again")
	return c
}
