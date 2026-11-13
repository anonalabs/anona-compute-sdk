package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
)

// The credential vault. A value is write-only over the API: it goes in, and no
// response on any route carries it back. So there is no `vault get` and no
// `vault show` here, and there never should be -- the sandbox that uses the
// secret does not see it either, which is the whole point.
//
// Values are read from a file or from stdin, never from an argument. A flag
// value lands in shell history and in the process table, where every other user
// of the machine can read it with ps.

// vaultInjectRE parses --vault-inject HOST:HEADER=[bearer:|basic:]SECRET.
// The header name is HTTP's token set; the secret name is the vault's
// (the gateway's rule: lowercase, digits, dash, underscore).
var vaultInjectRE = regexp.MustCompile(`^([^:=\s]+):([A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+)=(?:(bearer|basic):)?([a-z][a-z0-9_-]{0,63})$`)

func newVaultCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "vault",
		Short: "Store credentials the sandbox never sees",
		Long: `Manage the organization's credential vault.

A secret stored here is injected into the sandbox's outbound requests by the
egress proxy, on the host, after the sandbox has handed the request over. The
sandbox cannot read it, and neither can this CLI: no response on any vault route
carries a value, so there is no "get" command.

A secret records the hosts it may be sent to. That list is not a convenience --
without it, anyone who can write an egress rule could aim your credential at a
server they control and read it out of their own access log. A secret with no
list cannot be used at all.

To use a secret, name it in a sandbox's network rules:

  anona-compute vault create gh_token --host api.github.com --value-file token.txt
  anona-compute create --no-internet \
    --vault-inject api.github.com:Authorization=bearer:gh_token

Creating a new binding needs an owner or admin login, or an API key with the
vault:bind scope. That scope is in no preset, including full_access: it has to
be chosen deliberately when the key is made.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(
		newVaultListCmd(a),
		newVaultCreateCmd(a),
		newVaultRotateCmd(a),
		newVaultHostsCmd(a),
		newVaultDeleteCmd(a),
		newVaultUnbindCmd(a),
	)
	return c
}

func newVaultListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List secrets, their allowed hosts and their bindings",
		Long: `List the organization's secrets (GET /v1/vault/secrets).

Values are never listed, because the API never returns one. A secret with no
allowed hosts is shown as such: it predates the allowed-hosts list and nothing
may use it until "vault allowed-hosts" sets one.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			l, err := cl.ListVaultSecrets(a.ctx)
			if err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]any{"enabled": l.Enabled, "secrets": vaultSecretViews(l.Secrets)})
				return nil
			}
			if !l.Enabled {
				fmt.Fprintln(a.stderr, "anona-compute: the credential vault is not configured on this gateway; secrets cannot be stored")
			}
			if len(l.Secrets) == 0 {
				fmt.Fprintln(a.stdout, "no secrets")
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tVERSION\tALLOWED HOSTS\tBINDINGS\tUPDATED")
			for _, s := range l.Secrets {
				hosts := strings.Join(s.AllowedHosts, ",")
				if hosts == "" {
					hosts = "(none: unusable until set)"
				}
				fmt.Fprintf(tw, "%s\tv%d\t%s\t%d\t%s\n", s.Name, s.Version, hosts, len(s.Bindings),
					s.UpdatedAt.UTC().Format(time.RFC3339))
			}
			tw.Flush()
			for _, s := range l.Secrets {
				if len(s.Bindings) == 0 {
					continue
				}
				fmt.Fprintf(a.stdout, "\n%s bindings:\n", s.Name)
				bt := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(bt, "  ID\tHOST\tHEADER\tFORMAT\tSCOPE")
				for _, b := range s.Bindings {
					scope := b.Scope
					if b.ScopeID != "" {
						scope += " " + b.ScopeID
					}
					fmt.Fprintf(bt, "  %s\t%s\t%s\t%s\t%s\n", b.ID, b.Host, b.Header, b.Format, scope)
				}
				bt.Flush()
			}
			return nil
		},
	}
}

func newVaultCreateCmd(a *app) *cobra.Command {
	var hosts []string
	var file string
	c := &cobra.Command{
		Use:   "create <name>",
		Short: "Store a new secret",
		Long: `Store a new secret (POST /v1/vault/secrets).

The value is read from --value-file, or from stdin when that is not a terminal.
It is never taken from a flag: a flag value is in your shell history and in the
process table, where any other user of the machine can read it with ps.

A single trailing newline is stripped, so a value written with a text editor or
printed by another command does not carry one into the Authorization header.

--host is required and repeatable. An entry is an exact name (api.github.com), a
subdomain wildcard (*.github.com, which does not cover the apex), or * on its own
for any host, which has to be chosen deliberately.

A name that already exists is a 409: use "vault rotate" to add a version.`,
		Args: exactArgs(1, "<name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(hosts) == 0 {
				return usageErr("--host is required: a secret with no allowed hosts cannot be used by anything")
			}
			value, err := a.readSecretValue(file)
			if err != nil {
				return err
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			s, err := cl.CreateVaultSecret(a.ctx, args[0], value, hosts)
			if err != nil {
				return err
			}
			return a.reportVaultSecret("stored", s)
		},
	}
	c.Flags().StringArrayVar(&hosts, "host", nil, "a host this secret may be sent to (repeatable; required)")
	c.Flags().StringVar(&file, "value-file", "", "file holding the value (\"-\" or omitted reads stdin)")
	return c
}

func newVaultRotateCmd(a *app) *cobra.Command {
	var hosts []string
	var file string
	c := &cobra.Command{
		Use:   "rotate <name>",
		Short: "Store a new version of a secret and make it current",
		Long: `Store a new version of a secret and make it current
(POST /v1/vault/secrets/{name}/versions).

Existing bindings keep working and start resolving to the new value; nothing has
to be recreated. Without --host the recorded allowed-hosts list is kept.

The value is read from --value-file or stdin, as with "vault create".`,
		Args: exactArgs(1, "<name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			value, err := a.readSecretValue(file)
			if err != nil {
				return err
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			s, err := cl.RotateVaultSecret(a.ctx, args[0], value, hosts)
			if err != nil {
				return err
			}
			return a.reportVaultSecret("rotated", s)
		},
	}
	c.Flags().StringArrayVar(&hosts, "host", nil, "replace the allowed hosts as well (repeatable)")
	c.Flags().StringVar(&file, "value-file", "", "file holding the new value (\"-\" or omitted reads stdin)")
	return c
}

func newVaultHostsCmd(a *app) *cobra.Command {
	var hosts []string
	c := &cobra.Command{
		Use:   "allowed-hosts <name>",
		Short: "Replace the hosts a secret may be sent to",
		Long: `Replace the hosts a secret may be injected to
(PUT /v1/vault/secrets/{name}/allowed-hosts).

The list is replaced, not added to. Narrowing it takes effect in seconds: the
host agents poll for narrowed authorization and drop the cached value, rather
than waiting out its TTL.

A secret stored before allowed hosts existed has none recorded, and nothing may
use it until this command sets a list.`,
		Args: exactArgs(1, "<name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(hosts) == 0 {
				return usageErr("--host is required: an empty list would leave the secret unusable")
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			s, err := cl.SetVaultAllowedHosts(a.ctx, args[0], hosts)
			if err != nil {
				return err
			}
			return a.reportVaultSecret("updated", s)
		},
	}
	c.Flags().StringArrayVar(&hosts, "host", nil, "a host this secret may be sent to (repeatable; required)")
	return c
}

func newVaultDeleteCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "delete <name>",
		Aliases: []string{"rm"},
		Short:   "Delete a secret and every binding of it",
		Long: `Delete a secret (DELETE /v1/vault/secrets/{name}).

Every binding of it stops resolving. A sandbox whose policy still names it does
not silently lose the header: the egress proxy refuses those requests rather
than forward them without the credential.`,
		Args: exactArgs(1, "<name>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			if err := cl.DeleteVaultSecret(a.ctx, args[0]); err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]any{"name": args[0], "deleted": true})
				return nil
			}
			fmt.Fprintf(a.stdout, "deleted %s\n", args[0])
			return nil
		},
	}
}

func newVaultUnbindCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "unbind <binding-id>",
		Short: "Revoke one binding, leaving the secret",
		Long: `Revoke one binding (DELETE /v1/vault/bindings/{id}).

The secret stays; only this one injection stops. "vault list" prints the IDs.`,
		Args: exactArgs(1, "<binding-id>"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			if err := cl.RevokeVaultBinding(a.ctx, args[0]); err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]any{"binding_id": args[0], "revoked": true})
				return nil
			}
			fmt.Fprintf(a.stdout, "revoked %s\n", args[0])
			return nil
		},
	}
}

// readSecretValue reads a secret value from a file or stdin. It refuses to read
// from a terminal: a person typing at a prompt leaves the value on their screen
// and in their scrollback, and the shape of this CLI is pipes and files.
func (a *app) readSecretValue(file string) (string, error) {
	var (
		b   []byte
		err error
	)
	if file == "" || file == "-" {
		if f, ok := a.stdin.(*os.File); ok {
			st, serr := f.Stat()
			if serr == nil && st.Mode()&os.ModeCharDevice != 0 {
				return "", usageErr("no value on stdin: pass --value-file FILE, or pipe the value in")
			}
		}
		b, err = io.ReadAll(io.LimitReader(a.stdin, 1<<20))
	} else {
		b, err = os.ReadFile(file)
	}
	if err != nil {
		return "", fmt.Errorf("read the secret value: %w", err)
	}
	// One trailing newline only. Trimming all whitespace would quietly change
	// a value that legitimately ends in one.
	s := strings.TrimSuffix(string(b), "\n")
	s = strings.TrimSuffix(s, "\r")
	if s == "" {
		return "", usageErr("the secret value is empty")
	}
	return s, nil
}

func (a *app) reportVaultSecret(verb string, s *api.VaultSecret) error {
	if a.json() {
		a.emit(vaultSecretView(*s))
		return nil
	}
	fmt.Fprintf(a.stdout, "%s %s (v%d), allowed on %s\n", verb, s.Name, s.Version,
		strings.Join(s.AllowedHosts, ", "))
	return nil
}

func vaultSecretView(s api.VaultSecret) map[string]any {
	bs := make([]map[string]any, 0, len(s.Bindings))
	for _, b := range s.Bindings {
		bs = append(bs, map[string]any{
			"id": b.ID, "host": b.Host, "header": b.Header, "format": b.Format,
			"scope": b.Scope, "scope_id": b.ScopeID, "created_at": b.CreatedAt,
		})
	}
	return map[string]any{
		"name": s.Name, "version": s.Version, "allowed_hosts": s.AllowedHosts,
		"created_at": s.CreatedAt, "updated_at": s.UpdatedAt, "bindings": bs,
	}
}

func vaultSecretViews(in []api.VaultSecret) []map[string]any {
	out := make([]map[string]any, 0, len(in))
	for _, s := range in {
		out = append(out, vaultSecretView(s))
	}
	return out
}

// buildNetwork turns the create command's network flags into the `network`
// document of the create request. It returns nil when none were given, so a
// create with no network flags sends exactly what it sent before.
//
// --network-file wins outright and is passed through unchanged: the egress spec
// has more in it than flags should try to cover (rules_l7, dnsServers, ingress),
// and the gateway validates it properly anyway.
func buildNetwork(file string, allowOut, injects []string) (json.RawMessage, error) {
	if file != "" {
		var b []byte
		var err error
		if file == "-" {
			b, err = io.ReadAll(os.Stdin)
		} else {
			b, err = os.ReadFile(file)
		}
		if err != nil {
			return nil, fmt.Errorf("--network-file: %w", err)
		}
		if !json.Valid(b) {
			return nil, usageErr("--network-file: %s is not valid JSON", file)
		}
		if len(allowOut) > 0 || len(injects) > 0 {
			return nil, usageErr("--network-file replaces the whole network document; do not combine it with --allow-out or --vault-inject")
		}
		return json.RawMessage(b), nil
	}
	if len(allowOut) == 0 && len(injects) == 0 {
		return nil, nil
	}
	type transform struct {
		Headers map[string]string `json:"headers"`
	}
	type rule struct {
		Transform *transform `json:"transform"`
	}
	doc := struct {
		AllowOut []string          `json:"allowOut,omitempty"`
		Rules    map[string][]rule `json:"rules,omitempty"`
	}{AllowOut: allowOut}

	// One rule per host, with every header for that host in it: the compiler
	// takes a list per host, and splitting two headers of the same host across
	// two entries would be two bindings where one is meant.
	headers := map[string]map[string]string{}
	for _, in := range injects {
		m := vaultInjectRE.FindStringSubmatch(in)
		if m == nil {
			return nil, usageErr("--vault-inject %q: want HOST:HEADER=[bearer:|basic:]SECRET, for example api.github.com:Authorization=bearer:gh_token", in)
		}
		host, header, format, secret := m[1], m[2], m[3], m[4]
		value := "${vault." + secret + "}"
		switch format {
		case "bearer":
			value = "Bearer " + value
		case "basic":
			value = "Basic " + value
		}
		if headers[host] == nil {
			headers[host] = map[string]string{}
		}
		if _, dup := headers[host][header]; dup {
			return nil, usageErr("--vault-inject: %s is given twice for %s", header, host)
		}
		headers[host][header] = value
	}
	if len(headers) > 0 {
		doc.Rules = map[string][]rule{}
		hosts := make([]string, 0, len(headers))
		for h := range headers {
			hosts = append(hosts, h)
		}
		sort.Strings(hosts)
		for _, h := range hosts {
			doc.Rules[h] = []rule{{Transform: &transform{Headers: headers[h]}}}
		}
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}
