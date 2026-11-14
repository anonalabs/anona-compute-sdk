package cli

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
)

// endsAt formats a sandbox deadline. The gateway reports endAt 9999-12-31 for
// a sandbox created with timeout -1, which has no deadline at all; printing
// that date read as "ends in the year 10000" (the console shows "no limit").
// A missing endAt (the zero time) is treated the same way.
func endsAt(t time.Time, layout string) string {
	if noDeadline(t) {
		return "never"
	}
	return t.Local().Format(layout)
}

func noDeadline(t time.Time) bool { return t.IsZero() || t.UTC().Year() >= 9000 }

// sandboxView is what the CLI prints for a sandbox. Access tokens are never
// printed.
type sandboxView struct {
	SandboxID string `json:"sandbox_id"`
	// Name is the sandbox's human name (metadata "name"); the ID stays the
	// identifier.
	Name        string            `json:"name,omitempty"`
	TemplateID  string            `json:"template_id,omitempty"`
	Alias       string            `json:"alias,omitempty"`
	State       string            `json:"state,omitempty"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	EndAt       *time.Time        `json:"end_at,omitempty"`
	CPUCount    int32             `json:"cpu_count,omitempty"`
	MemoryMB    int32             `json:"memory_mb,omitempty"`
	DiskSizeMB  int32             `json:"disk_size_mb,omitempty"`
	Size        string            `json:"size,omitempty"`
	EnvdVersion string            `json:"envd_version,omitempty"`
	Domain      string            `json:"domain,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func viewOfSandbox(s *api.Sandbox) sandboxView {
	v := sandboxView{SandboxID: s.SandboxID, TemplateID: s.TemplateID, Alias: s.Alias, EnvdVersion: s.EnvdVersion}
	if s.Domain != nil {
		v.Domain = *s.Domain
	}
	return v
}

func viewOfDetail(s *api.SandboxDetail) sandboxView {
	v := sandboxView{SandboxID: s.SandboxID, Name: s.Metadata[api.MetaName], TemplateID: s.TemplateID, Alias: s.Alias, State: s.State,
		StartedAt: &s.StartedAt, EndAt: &s.EndAt, CPUCount: s.CPUCount, MemoryMB: s.MemoryMB, DiskSizeMB: s.DiskSizeMB,
		Size: s.Size, EnvdVersion: s.EnvdVersion, Metadata: s.Metadata}
	if s.Domain != nil {
		v.Domain = *s.Domain
	}
	return v
}

func viewOfListed(s *api.ListedSandbox) sandboxView {
	return sandboxView{SandboxID: s.SandboxID, Name: s.Metadata[api.MetaName], TemplateID: s.TemplateID, Alias: s.Alias, State: s.State,
		StartedAt: &s.StartedAt, EndAt: &s.EndAt, CPUCount: s.CPUCount, MemoryMB: s.MemoryMB, DiskSizeMB: s.DiskSizeMB,
		Size: s.Size, EnvdVersion: s.EnvdVersion, Metadata: s.Metadata}
}

// parseSeconds accepts a Go duration ("300s", "5m", "1h") or a plain number
// of seconds.
func parseSeconds(s string, allowZero bool) (int32, error) {
	var d time.Duration
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		d = time.Duration(n) * time.Second
	} else {
		d, err = time.ParseDuration(s)
		if err != nil {
			return 0, fmt.Errorf("invalid duration %q (use e.g. 300s, 10m, 1h)", s)
		}
	}
	if d < 0 || (!allowZero && d < time.Second) {
		return 0, fmt.Errorf("duration %q is too short", s)
	}
	secs := d / time.Second
	if secs > math.MaxInt32 {
		return 0, fmt.Errorf("duration %q is too long", s)
	}
	return int32(secs), nil
}

// maxNameLen is the longest sandbox name, in characters.
const maxNameLen = 64

// cleanName trims a sandbox name and checks it against the gateway's rule:
// 1 to 64 letters, digits,
// spaces, dots, dashes or underscores, starting with a letter or digit. ""
// means no name.
func cleanName(raw, what string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", nil
	}
	bad := utf8.RuneCountInString(name) > maxNameLen
	for i, r := range name {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || (i > 0 && strings.ContainsRune(" .-_", r))
		bad = bad || !ok
	}
	if bad {
		return "", usageErr("%s must be 1 to %d letters, digits, spaces, dots, dashes or underscores, starting with a letter or digit; got %q", what, maxNameLen, raw)
	}
	return name, nil
}

func parseKV(pairs []string, what string) (map[string]string, error) {
	if len(pairs) == 0 {
		return nil, nil
	}
	m := make(map[string]string, len(pairs))
	for _, p := range pairs {
		k, v, ok := strings.Cut(p, "=")
		if !ok || k == "" {
			return nil, usageErr("%s must be KEY=VALUE, got %q", what, p)
		}
		m[k] = v
	}
	return m, nil
}

type createOpts struct {
	template   string
	timeout    string
	envs       []string
	metadata   []string
	name       string
	noInternet bool
	autoPause  bool
	idlePause  string
	count      int
	failFast   bool
	size       string
	// parallel, createTimeout and capacityWait shape a --count batch: how
	// many creates run at once, how long one may take, and how long one
	// waits while the server has no capacity yet.
	parallel      int
	createTimeout time.Duration
	capacityWait  time.Duration

	// Egress policy. networkFile is the whole document; allowOut and
	// vaultInject cover the two things people actually reach for.
	networkFile string
	allowOut    []string
	vaultInject []string
}

func (o *createOpts) register(c *cobra.Command) {
	f := c.Flags()
	f.StringVarP(&o.template, "template", "t", "", "template ID or alias (default: profile default_template, else \"base\")")
	f.StringVar(&o.size, "size", "", "sandbox size: small (1 vCPU, 2 GiB, 10 GB disk), medium (2, 4 GiB, 20 GB), large (4, 8 GiB, 40 GB) or xl (8, 16 GiB, 80 GB); default: the template's own size (on Free, the smallest size the template allows)")
	f.StringVar(&o.timeout, "timeout", "300s", "sandbox lifetime before it is deleted or paused (e.g. 300s, 1h), or \"never\" for no time limit")
	f.StringArrayVarP(&o.envs, "env", "e", nil, "environment variable KEY=VALUE for the sandbox (repeatable)")
	f.StringArrayVar(&o.metadata, "metadata", nil, "metadata KEY=VALUE (repeatable)")
	f.StringVar(&o.name, "name", "", "a name for the sandbox, shown beside its ID (stored as metadata name=NAME; the ID stays the identifier)")
	f.BoolVar(&o.noInternet, "no-internet", false, "create the sandbox without internet access")
	f.BoolVar(&o.autoPause, "auto-pause", false, "pause instead of delete when the timeout ends")
	f.StringVar(&o.idlePause, "idle-pause", "",
		"pause the sandbox after this long with nothing using it (e.g. 15m, 1h); \"off\" disables it. A duration also makes the timeout pause the sandbox instead of deleting it, as --auto-pause does")
	f.StringArrayVar(&o.allowOut, "allow-out", nil, "host the sandbox may reach (repeatable); with --no-internet this is the allow-list")
	f.StringArrayVar(&o.vaultInject, "vault-inject", nil,
		"inject a vault secret into requests to a host: HOST:HEADER=[bearer:|basic:]SECRET (repeatable)")
	f.StringVar(&o.networkFile, "network-file", "", "JSON egress policy for the sandbox (\"-\" reads stdin); replaces the flags above")
}

func (o *createOpts) request(defaultTemplate string) (api.NewSandboxV2, error) {
	req := api.NewSandboxV2{TemplateID: o.template}
	if req.TemplateID == "" {
		req.TemplateID = defaultTemplate
	}
	if o.size != "" {
		sz, ok := normalizeSize(o.size)
		if !ok {
			return req, usageErr("--size %q: use small, medium, large or xl", o.size)
		}
		req.Size = sz
	}
	// "never" (or -1) is the API's timeout -1, no deadline at all: the same
	// spelling the timeout command takes. The gateway refuses it when the
	// team has a maximum sandbox lifetime.
	var secs int32 = -1
	var err error
	if o.timeout != "never" && o.timeout != "-1" {
		if secs, err = parseSeconds(o.timeout, false); err != nil {
			return req, usageErr("--timeout: %v; give a duration such as 300s or 1h, or \"never\"", err)
		}
	}
	req.Timeout = &secs
	if req.EnvVars, err = parseKV(o.envs, "--env"); err != nil {
		return req, err
	}
	if req.Metadata, err = parseKV(o.metadata, "--metadata"); err != nil {
		return req, err
	}
	if o.name != "" {
		name, err := cleanName(o.name, "--name")
		if err != nil {
			return req, err
		}
		if v, ok := req.Metadata[api.MetaName]; ok && v != name {
			return req, usageErr("--name %q and --metadata %s=%q disagree; give one", name, api.MetaName, v)
		}
		if name != "" {
			if req.Metadata == nil {
				req.Metadata = map[string]string{}
			}
			req.Metadata[api.MetaName] = name
		}
	}
	if o.noInternet {
		f := false
		req.AllowInternetAccess = &f
	}
	if o.autoPause {
		req.AutoPause = &api.AutoPause{On: true}
	}
	if o.idlePause != "" {
		mins, err := parseIdleMinutes(o.idlePause)
		if err != nil {
			return req, err
		}
		// The gateway reads the object form {"idleMinutes": n} as pause-on-
		// timeout too. Without
		// --auto-pause, "off" therefore sends nothing: no autoPause already
		// means no idle pausing, and {"idleMinutes": 0} would have turned the
		// timeout's delete into a pause.
		if mins > 0 || req.AutoPause != nil {
			if req.AutoPause == nil {
				req.AutoPause = &api.AutoPause{On: true}
			}
			req.AutoPause.IdleMinutes = &mins
		}
	}
	if req.Network, err = buildNetwork(o.networkFile, o.allowOut, o.vaultInject); err != nil {
		return req, err
	}
	return req, nil
}

func newCreateCmd(a *app) *cobra.Command {
	o := &createOpts{}
	c := &cobra.Command{
		Use:     "create",
		Aliases: []string{"new"},
		Short:   "Create sandboxes",
		Long: `Create one or more sandboxes (POST /v2/sandboxes) and print their IDs.

--size picks the sandbox's vCPU, memory and disk: small, medium, large or xl.
Every template has a minimum size, and a size below it, or one your plan does
not include, is refused with the sizes that would work (` + "`anona-compute template info`" + `
lists them). A template your team built has its own vCPU, memory and disk as
its minimum. A built-in template's minimum is set for it instead: small for
base, python, node and code-interpreter, medium for desktop and agent-desktop.
Without --size the sandbox runs at the template's own size (on Free, the
smallest size the template allows: small for base). The first sandbox
at a size a template was not built at takes longer while that size is
prepared.

A create is retried only when the server says it is safe (Should-Retry: true,
waiting Retry-After). When the server has no capacity yet (a host is starting,
or the sandbox server is waking), a create waits up to --capacity-wait (10
minutes by default) and prints a progress line on stderr each time it retries.
A 502, 503 or 504 without that header is not retried, because the sandbox may
already have started: the command fails saying so, and you should list your
sandboxes before creating again.

With --count, up to --parallel creates run at once and each has its own
--create-timeout, so one slow or failing create does not hold up the others.
Each sandbox is printed as it starts; failures are reported on stderr with a
summary, and the command exits non-zero if any create failed.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if o.count < 1 || o.count > 100 {
				return usageErr("--count must be between 1 and 100")
			}
			if o.parallel < 1 || o.parallel > 20 {
				return usageErr("--parallel must be between 1 and 20")
			}
			if o.createTimeout <= 0 {
				return usageErr("--create-timeout must be positive")
			}
			cl, s, err := a.client()
			if err != nil {
				return err
			}
			req, err := o.request(s.Template)
			if err != nil {
				return err
			}
			var outMu sync.Mutex
			a.waitForCapacity(cl, o.capacityWait, &outMu)
			return o.createBatch(a, cl, req, &outMu)
		},
	}
	o.register(c)
	c.Flags().IntVar(&o.count, "count", 1, "number of sandboxes to create (1-100)")
	c.Flags().BoolVar(&o.failFast, "fail-fast", false, "with --count, stop at the first failure")
	c.Flags().IntVar(&o.parallel, "parallel", 4, "with --count, how many creates run at once (1-20)")
	c.Flags().DurationVar(&o.createTimeout, "create-timeout", 15*time.Minute,
		"give up on one create after this long, waits for capacity included")
	capacityWaitFlag(c, &o.capacityWait, "create")
	return c
}

// capacityWaitFlag registers --capacity-wait on c, whose requests are a
// create or a resume (what names it in the help).
func capacityWaitFlag(c *cobra.Command, d *time.Duration, what string) {
	c.Flags().DurationVar(d, "capacity-wait", envDuration("ANONA_COMPUTE_CAPACITY_WAIT", api.DefaultCapacityWait),
		"how long a "+what+" waits while the server has no capacity yet, e.g. a host is starting (0 = do not wait; env ANONA_COMPUTE_CAPACITY_WAIT)")
}

// waitForCapacity sets cl's capacity wait from --capacity-wait (0 turns the
// long wait off) and prints a progress line on stderr before each wait, so
// --json output on stdout stays clean. mu serialises those lines with the
// command's own output.
func (a *app) waitForCapacity(cl *api.Client, wait time.Duration, mu *sync.Mutex) {
	cl.CapacityWait = wait
	if cl.CapacityWait == 0 {
		cl.CapacityWait = -1 // --capacity-wait 0: no long wait
	}
	cl.OnCapacityWait = func(code string, next, elapsed time.Duration) {
		why := "no host has room yet; a host may be starting"
		switch code {
		case "sandbox_engine_starting":
			why = "the sandbox server is starting"
		case "sandbox_size_preparing":
			why = "the template is being prepared at this size"
		}
		mu.Lock()
		fmt.Fprintf(a.stderr, "anona-compute: waiting for capacity: %s, retrying in %s (%s elapsed)\n",
			why, next.Round(time.Second), elapsed.Round(time.Second))
		mu.Unlock()
	}
}

// envDuration reads a duration from the environment, or returns def.
func envDuration(name string, def time.Duration) time.Duration {
	if v, err := time.ParseDuration(os.Getenv(name)); err == nil && v >= 0 {
		return v
	}
	return def
}

// createBatch starts o.count sandboxes, at most o.parallel at a time, each
// with its own deadline, so one slow or failing create never holds up the
// others. Each result is printed as it arrives; with more than one create, a
// summary follows on stderr. It fails if any create failed, but every
// sandbox that did start is printed first.
func (o *createOpts) createBatch(a *app, cl *api.Client, req api.NewSandboxV2, outMu *sync.Mutex) error {
	// stop is set by --fail-fast: no further creates start, but the ones in
	// flight finish and are reported, because cancelling a create half way
	// could leave a started sandbox that is never printed.
	var stop atomic.Bool
	sem := make(chan struct{}, o.parallel)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var ok int
	var errs []error
	for i := 0; i < o.count && !stop.Load() && a.ctx.Err() == nil; i++ {
		sem <- struct{}{}
		if stop.Load() {
			<-sem
			break
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			cctx, ccancel := context.WithTimeout(a.ctx, o.createTimeout)
			sb, err := cl.CreateSandbox(cctx, req)
			ccancel()
			if err != nil && errors.Is(err, context.DeadlineExceeded) && a.ctx.Err() == nil {
				err = fmt.Errorf("create timed out after %s (--create-timeout); list your sandboxes before retrying, it may still have started", o.createTimeout)
			}
			mu.Lock()
			if err != nil {
				errs = append(errs, err)
			} else {
				ok++
			}
			mu.Unlock()
			outMu.Lock()
			defer outMu.Unlock()
			if err != nil {
				if o.count > 1 {
					a.reportItemError("create", "", err)
				}
				if o.failFast {
					stop.Store(true)
				}
				return
			}
			if a.json() {
				v := viewOfSandbox(sb)
				v.Name = req.Metadata[api.MetaName]
				a.emit(v)
			} else {
				fmt.Fprintln(a.stdout, sb.SandboxID)
			}
		}()
	}
	wg.Wait()
	if len(errs) == 0 {
		return nil
	}
	if o.count == 1 {
		return errs[0]
	}
	if !a.json() {
		fmt.Fprintf(a.stderr, "anona-compute: created %d of %d; %d failed\n", ok, o.count, len(errs))
	}
	return errs[0]
}

// reportItemError reports one failed item of a multi-item command and lets
// the command continue.
func (a *app) reportItemError(op, id string, err error) {
	if a.json() {
		obj := errorObject(err, exitCodeFor(err))
		obj["op"] = op
		if id != "" {
			obj["sandbox_id"] = id
		}
		a.emit(map[string]any{"error": obj})
		return
	}
	// "kill" is the machine-readable op of a delete; people read "delete".
	verb := op
	if op == "kill" {
		verb = "delete"
	}
	if rid := requestID(err); rid != "" {
		err = fmt.Errorf("%w (request %s)", err, rid)
	}
	if id != "" {
		fmt.Fprintf(a.stderr, "anona-compute: %s %s: %v\n", verb, id, err)
	} else {
		fmt.Fprintf(a.stderr, "anona-compute: %s: %v\n", verb, err)
	}
}

func listStates(state string) ([]string, error) {
	switch state {
	case "running":
		return []string{"running"}, nil
	case "paused":
		return []string{"paused"}, nil
	case "all":
		return []string{"running", "paused"}, nil
	}
	return nil, usageErr("--state must be running, paused or all")
}

// listAll pages through GET /v2/sandboxes until there is no X-Next-Token or
// limit items were read (limit 0 = no limit).
func (a *app) listAll(cl *api.Client, p api.ListParams, limit int) ([]api.ListedSandbox, error) {
	var all []api.ListedSandbox
	for {
		p.Limit = 100
		if limit > 0 && limit-len(all) < 100 {
			p.Limit = limit - len(all)
		}
		page, next, err := cl.ListSandboxes(a.ctx, p)
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if next == "" || (limit > 0 && len(all) >= limit) || len(page) == 0 {
			break
		}
		p.NextToken = next
	}
	if limit > 0 && len(all) > limit {
		all = all[:limit]
	}
	return all, nil
}

func newListCmd(a *app) *cobra.Command {
	var state, name string
	var metadata []string
	var limit int
	c := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List sandboxes",
		Args:    noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			states, err := listStates(state)
			if err != nil {
				return err
			}
			md, err := parseKV(metadata, "--metadata")
			if err != nil {
				return err
			}
			if name = strings.TrimSpace(name); name != "" {
				if md == nil {
					md = map[string]string{}
				}
				md[api.MetaName] = name
			}
			if limit < 0 {
				return usageErr("--limit must be 0 or more")
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			items, err := a.listAll(cl, api.ListParams{States: states, Metadata: md}, limit)
			if err != nil {
				return err
			}
			if a.json() {
				views := make([]any, 0, len(items))
				for i := range items {
					views = append(views, viewOfListed(&items[i]))
				}
				emitList(a, views)
				return nil
			}
			if len(items) == 0 {
				fmt.Fprintln(a.stderr, "No sandboxes.")
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "SANDBOX ID\tNAME\tTEMPLATE\tSTATE\tCPU\tMEMORY\tSTARTED\tENDS")
			for _, s := range items {
				tmpl := s.TemplateID
				if s.Alias != "" {
					tmpl = s.Alias
				}
				label := s.Metadata[api.MetaName]
				if label == "" {
					label = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d MiB\t%s\t%s\n", s.SandboxID, label, tmpl, s.State, s.CPUCount, s.MemoryMB,
					s.StartedAt.Local().Format(time.DateTime), endsAt(s.EndAt, time.DateTime))
			}
			return tw.Flush()
		},
	}
	c.Flags().StringVarP(&state, "state", "s", "running", "running, paused or all")
	c.Flags().StringArrayVar(&metadata, "metadata", nil, "only sandboxes with metadata KEY=VALUE (repeatable)")
	c.Flags().StringVar(&name, "name", "", "only sandboxes with this exact name (same as --metadata name=NAME)")
	c.Flags().IntVar(&limit, "limit", 0, "show at most N sandboxes (0 = all)")
	return c
}

func newInfoCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "info SANDBOX_ID",
		Short: "Show one sandbox",
		Args:  exactArgs(1, "SANDBOX_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			d, err := cl.GetSandbox(a.ctx, args[0])
			if err != nil {
				return err
			}
			v := viewOfDetail(d)
			if a.json() {
				a.emit(v)
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "Sandbox ID\t%s\n", v.SandboxID)
			if v.Name != "" {
				fmt.Fprintf(tw, "Name\t%s\n", v.Name)
			}
			fmt.Fprintf(tw, "Template\t%s\n", v.TemplateID)
			if v.Alias != "" {
				fmt.Fprintf(tw, "Alias\t%s\n", v.Alias)
			}
			fmt.Fprintf(tw, "State\t%s\n", v.State)
			if v.Size != "" {
				fmt.Fprintf(tw, "Size\t%s\n", v.Size)
			}
			fmt.Fprintf(tw, "CPU\t%d\n", v.CPUCount)
			fmt.Fprintf(tw, "Memory\t%d MiB\n", v.MemoryMB)
			fmt.Fprintf(tw, "Disk\t%d MiB\n", v.DiskSizeMB)
			fmt.Fprintf(tw, "Started\t%s\n", d.StartedAt.Local().Format(time.RFC3339))
			if noDeadline(d.EndAt) {
				fmt.Fprintf(tw, "Ends\tnever (no time limit)\n")
			} else {
				fmt.Fprintf(tw, "Ends\t%s\n", d.EndAt.Local().Format(time.RFC3339))
			}
			fmt.Fprintf(tw, "envd\t%s\n", v.EnvdVersion)
			for k, val := range v.Metadata {
				fmt.Fprintf(tw, "Metadata\t%s=%s\n", k, val)
			}
			return tw.Flush()
		},
	}
}

// newKillCmd is the delete command. The user-facing verb is "delete";
// "kill" stays an alias because it is E2B's verb and scripts already call it, and
// so does "rm". The machine-readable result ({"status":"killed"}), the API call
// and the MCP tool name are unchanged.
func newKillCmd(a *app) *cobra.Command {
	var all, yes bool
	c := &cobra.Command{
		Use:     "delete SANDBOX_ID...",
		Aliases: []string{"kill", "rm"},
		Short:   "Delete sandboxes",
		Args: func(cmd *cobra.Command, args []string) error {
			if all && len(args) > 0 {
				return usageErr("use either --all or sandbox IDs, not both")
			}
			if !all && len(args) == 0 {
				return usageErr("usage: anona-compute delete SANDBOX_ID... (or --all --yes)")
			}
			if all && !yes {
				return usageErr("--all deletes every running and paused sandbox; add --yes to confirm")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			ids := args
			if all {
				items, err := a.listAll(cl, api.ListParams{States: []string{"running", "paused"}}, 0)
				if err != nil {
					return err
				}
				for _, s := range items {
					ids = append(ids, s.SandboxID)
				}
			}
			var firstErr error
			for _, id := range ids {
				if err := cl.KillSandbox(a.ctx, id); err != nil {
					if firstErr == nil {
						firstErr = err
					}
					if len(ids) > 1 {
						a.reportItemError("kill", id, err) // "op" is machine-readable: unchanged
					}
					continue
				}
				if a.json() {
					a.emit(map[string]any{"sandbox_id": id, "status": "killed"})
				} else {
					fmt.Fprintf(a.stderr, "Deleted %s\n", id)
				}
			}
			if firstErr != nil && len(ids) > 1 {
				return silentExit(exitCodeFor(firstErr))
			}
			return firstErr
		},
	}
	c.Flags().BoolVar(&all, "all", false, "delete every running and paused sandbox")
	c.Flags().BoolVar(&yes, "yes", false, "confirm --all")
	return c
}

func newPauseCmd(a *app) *cobra.Command {
	var mode string
	c := &cobra.Command{
		Use:   "pause SANDBOX_ID",
		Short: "Pause a sandbox",
		Long: `Pause a sandbox (POST /sandboxes/{id}/pause). Memory and disk are kept, so
running processes continue after resume.

--mode filesystem (keep only the disk; resume cold-boots) is not yet
supported: the platform has no disk-only pause, and it is refused.`,
		Args: exactArgs(1, "SANDBOX_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			memory := true
			switch mode {
			case "full":
			case "filesystem":
				return usageErr("--mode filesystem is not yet supported: the platform has no disk-only pause; use --mode full (the default)")
			default:
				return usageErr("--mode must be full")
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			already, err := cl.PauseSandbox(a.ctx, args[0], memory)
			if err != nil {
				return err
			}
			status := "paused"
			if already {
				status = "already_paused"
			}
			if a.json() {
				a.emit(map[string]any{"sandbox_id": args[0], "status": status})
			} else if already {
				fmt.Fprintf(a.stderr, "%s is already paused\n", args[0])
			} else {
				fmt.Fprintf(a.stderr, "Paused %s\n", args[0])
			}
			return nil
		},
	}
	c.Flags().StringVar(&mode, "mode", "full", "full (memory and disk); filesystem (disk only) is not yet supported")
	return c
}

func newResumeCmd(a *app) *cobra.Command {
	var timeout string
	var capacityWait time.Duration
	c := &cobra.Command{
		Use:   "resume SANDBOX_ID",
		Short: "Resume a paused sandbox",
		Long: `Resume a paused sandbox (POST /v2/sandboxes/{id}/connect). A running
sandbox is left running; its timeout is only ever extended.

When the server has no capacity yet (a host is starting, or the sandbox
server is waking), a resume waits up to --capacity-wait (10 minutes by
default) and prints a progress line on stderr each time it retries.`,
		Args: exactArgs(1, "SANDBOX_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			secs, err := parseSeconds(timeout, false)
			if err != nil {
				return usageErr("--timeout: %v", err)
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			a.waitForCapacity(cl, capacityWait, &sync.Mutex{})
			sb, resumed, err := cl.ConnectSandbox(a.ctx, args[0], secs)
			if err != nil {
				return err
			}
			status := "running"
			if resumed {
				status = "resumed"
			}
			if a.json() {
				v := viewOfSandbox(sb)
				a.emit(map[string]any{"sandbox_id": v.SandboxID, "template_id": v.TemplateID, "status": status})
			} else if resumed {
				fmt.Fprintf(a.stderr, "Resumed %s\n", sb.SandboxID)
			} else {
				fmt.Fprintf(a.stderr, "%s was already running\n", sb.SandboxID)
			}
			return nil
		},
	}
	c.Flags().StringVar(&timeout, "timeout", "300s", "sandbox lifetime from now")
	capacityWaitFlag(c, &capacityWait, "resume")
	return c
}

func newTimeoutCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "timeout SANDBOX_ID DURATION",
		Aliases: []string{"extend"},
		Short:   "Set how long a sandbox lives from now",
		Long: `Set the sandbox timeout (POST /sandboxes/{id}/timeout). The sandbox ends
DURATION from now; each call replaces the previous value. DURATION is a
positive number of seconds or a duration (600, 10m, 2h), or "never" for no
time limit at all (the API's timeout -1, also accepted as "-- -1"). "never"
is refused when your team has a maximum sandbox lifetime.`,
		Args: exactArgs(2, "SANDBOX_ID DURATION"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var secs int32
			never := args[1] == "never" || args[1] == "-1"
			if never {
				secs = -1
			} else {
				var err error
				if secs, err = parseSeconds(args[1], false); err != nil {
					return usageErr("%v; DURATION is positive (e.g. 600, 10m, 2h) or never", err)
				}
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			if err := cl.SetTimeout(a.ctx, args[0], secs); err != nil {
				return err
			}
			if never {
				if a.json() {
					a.emit(map[string]any{"sandbox_id": args[0], "timeout": -1, "end_at": nil})
				} else {
					fmt.Fprintf(a.stderr, "%s now has no time limit\n", args[0])
				}
				return nil
			}
			endAt := time.Now().Add(time.Duration(secs) * time.Second).UTC()
			if a.json() {
				a.emit(map[string]any{"sandbox_id": args[0], "timeout": secs, "end_at": endAt.Format(time.RFC3339)})
			} else {
				fmt.Fprintf(a.stderr, "%s now ends at %s\n", args[0], endAt.Local().Format(time.DateTime))
			}
			return nil
		},
	}
}

func newRenameCmd(a *app) *cobra.Command {
	var clear bool
	c := &cobra.Command{
		Use:   "rename SANDBOX_ID [NAME]",
		Short: "Name a sandbox, or remove its name",
		Long: `Set a sandbox's name (PUT /v1/sandboxes/{id}/name). The name is shown
beside the ID in list, info and the console, and is stored as the metadata
key "name", so create --name and list --name use the same value. The
sandbox ID stays the identifier: every other command still takes the ID.

A name is 1 to 64 letters, digits, spaces, dots, dashes or underscores,
starting with a letter or digit. --clear removes it.`,
		Args: func(cmd *cobra.Command, args []string) error {
			switch {
			case len(args) == 0:
				return usageErr("usage: anona-compute rename SANDBOX_ID NAME (or --clear)")
			case clear && len(args) != 1:
				return usageErr("--clear takes no NAME")
			case !clear && len(args) != 2:
				return usageErr("usage: anona-compute rename SANDBOX_ID NAME (quote a name with spaces), or --clear")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if !clear {
				var err error
				if name, err = cleanName(args[1], "NAME"); err != nil {
					return err
				}
				if name == "" {
					return usageErr("NAME is empty; use --clear to remove a name")
				}
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			out, err := cl.SetSandboxName(a.ctx, args[0], name)
			if err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]any{"sandbox_id": out.SandboxID, "name": out.Name})
			} else if out.Name == "" {
				fmt.Fprintf(a.stderr, "%s has no name now\n", out.SandboxID)
			} else {
				fmt.Fprintf(a.stderr, "%s is now named %q\n", out.SandboxID, out.Name)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&clear, "clear", false, "remove the sandbox's name")
	return c
}

func newForkCmd(a *app) *cobra.Command {
	var count int
	var timeout string
	c := &cobra.Command{
		Use:   "fork SANDBOX_ID",
		Short: "Fork a running sandbox into new sandboxes",
		Long: `Fork a sandbox (POST /sandboxes/{id}/fork): checkpoint it with its memory
and start --count new sandboxes from that checkpoint. Prints one line per fork.

Like create, a fork is not retried on a 502, 503 or 504 unless the server
says Should-Retry: true, because the forks may already exist: list your
sandboxes before forking again.`,
		Args: exactArgs(1, "SANDBOX_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			// The E2B SDK allows 1..20 (MAX_FORK_COUNT); the spec allows up to 100.
			if count < 1 || count > 20 {
				return usageErr("--count must be between 1 and 20")
			}
			var tp *int32
			if timeout != "" {
				secs, err := parseSeconds(timeout, true)
				if err != nil {
					return usageErr("--timeout: %v", err)
				}
				tp = &secs
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			res, err := cl.ForkSandbox(a.ctx, args[0], int32(count), tp)
			if err != nil {
				if api.StatusOf(err) == 501 {
					return fmt.Errorf("this server does not support fork yet: %w", err)
				}
				return err
			}
			failed := 0
			for _, r := range res {
				switch {
				case r.Sandbox != nil:
					if a.json() {
						a.emit(viewOfSandbox(r.Sandbox))
					} else {
						fmt.Fprintln(a.stdout, r.Sandbox.SandboxID)
					}
				case r.Error != nil:
					failed++
					r.Error.Status = int(r.Error.Code)
					a.reportItemError("fork", "", r.Error)
				}
			}
			if failed > 0 {
				return &exitError{code: ExitError, err: errors.New(strconv.Itoa(failed) + " of " + strconv.Itoa(len(res)) + " forks failed")}
			}
			return nil
		},
	}
	c.Flags().IntVar(&count, "count", 1, "number of forks (1-20)")
	c.Flags().StringVar(&timeout, "timeout", "300s", "lifetime of each fork (empty = server default, 15s in the E2B spec)")
	return c
}

// parseIdleMinutes turns an --idle-pause value into whole minutes. "off", "0"
// and "none" mean off. The API's unit is minutes, so a duration that is not a
// whole number of them is refused rather than silently rounded.
func parseIdleMinutes(v string) (int, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "none", "0":
		return 0, nil
	}
	secs, err := parseSeconds(v, false)
	if err != nil {
		return 0, usageErr("--idle-pause: %v; give a duration such as 15m or 1h, or \"off\"", err)
	}
	if secs%60 != 0 {
		return 0, usageErr("--idle-pause: %s is not a whole number of minutes; the API counts idle time in minutes", v)
	}
	mins := int(secs / 60)
	if mins < 1 || mins > api.MaxIdlePauseMinutes {
		return 0, usageErr("--idle-pause: %s is outside 1m to %dm (one week)", v, api.MaxIdlePauseMinutes)
	}
	return mins, nil
}

func newIdlePauseCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "idle-pause SANDBOX_ID DURATION",
		Short: "Pause a sandbox once nothing has used it for a while",
		Long: `Set a sandbox's idle-pause time (PUT /v1/sandboxes/{id}/auto-pause).
The sandbox pauses once nothing has used it for DURATION, however much time its
timeout has left. DURATION is a whole number of minutes (15m, 1h, up to 7 days);
"off" turns idle pausing off.

This is not --auto-pause, which decides what happens when the TIMEOUT runs out.
A sandbox can have both.

What counts as use, restarting the clock: a request to any of the sandbox's
ports (except envd's /health), an open terminal, live view, SSH session or byte
pipe, and any process envd started that is still running. CPU the guest burns on
its own does not count, so a sandbox busy on work it started itself still pauses.

Resume it with "anona-compute resume SANDBOX_ID".`,
		Args: exactArgs(2, "SANDBOX_ID DURATION"),
		RunE: func(cmd *cobra.Command, args []string) error {
			mins, err := parseIdleMinutes(args[1])
			if err != nil {
				return err
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			acc, err := cl.SetIdlePause(a.ctx, args[0], mins)
			if err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]any{"sandbox_id": args[0], "idle_pause_minutes": acc.IdlePauseMinutes})
				return nil
			}
			if acc.IdlePauseMinutes == 0 {
				fmt.Fprintf(a.stderr, "%s no longer pauses when idle\n", args[0])
			} else {
				fmt.Fprintf(a.stderr, "%s pauses after %d idle minute(s)\n", args[0], acc.IdlePauseMinutes)
			}
			return nil
		},
	}
}

// normalizeSize accepts a size name in any case and its short forms.
func normalizeSize(s string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "small", "s":
		return "small", true
	case "medium", "m":
		return "medium", true
	case "large", "l":
		return "large", true
	case "xl", "xlarge", "x-large":
		return "xl", true
	}
	return "", false
}
