package cli

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
)

// newTemplateCmd is `anona-compute template` over the gateway's template
// build API.
func newTemplateCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:     "template",
		Aliases: []string{"templates", "tpl"},
		Short:   "Build and manage templates",
		Args:    noArgs,
		RunE:    func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(newTemplateBuildCmd(a), newTemplateListCmd(a), newTemplateInfoCmd(a), newTemplateDeleteCmd(a), newTemplateLogsCmd(a))
	return c
}

type templateView struct {
	TemplateID  string   `json:"template_id"`
	Names       []string `json:"names"`
	BuildID     string   `json:"build_id"`
	Status      string   `json:"status"`
	Phase       string   `json:"phase,omitempty"`
	CPUCount    int      `json:"cpu_count"`
	MemoryMB    int      `json:"memory_mb"`
	DiskSizeMB  int      `json:"disk_size_mb"`
	Builds      int      `json:"builds"`
	EnvdVersion string   `json:"envd_version"`
	Public      bool     `json:"public"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
	// ImageBytes is the current image, uncompressed; StorageBytes what the
	// template keeps (that build and the one before it). Omitted when the
	// gateway does not state them.
	ImageBytes   int64 `json:"image_bytes,omitempty"`
	StorageBytes int64 `json:"storage_bytes,omitempty"`
}

func viewOfTemplate(t *api.Template) templateView {
	return templateView{TemplateID: t.TemplateID, Names: t.Names, BuildID: t.BuildID, Status: t.BuildStatus, Phase: t.Phase,
		CPUCount: t.CPUCount, MemoryMB: t.MemoryMB, DiskSizeMB: t.DiskSizeMB, Builds: t.BuildCount, EnvdVersion: t.EnvdVersion,
		Public: t.Public, CreatedAt: t.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: t.UpdatedAt.UTC().Format(time.RFC3339),
		ImageBytes: t.ImageBytes, StorageBytes: t.StorageBytes}
}

// formatBytes states a size as the gateway does (templates.FormatBytes):
// "512 MiB", "4.2 GiB", "10 GiB"; "-" for an unknown one.
func formatBytes(n int64) string {
	const mib, gib = 1 << 20, 1 << 30
	switch {
	case n <= 0:
		return "-"
	case n < mib:
		return strconv.FormatInt((n+1023)>>10, 10) + " KiB"
	case n < gib:
		return strconv.FormatInt((n+mib/2)/mib, 10) + " MiB"
	}
	return gibString(n) + " GiB"
}

// gibString is n in GiB to one decimal, without a trailing ".0".
func gibString(n int64) string {
	s := strconv.FormatFloat(float64(n)/(1<<30), 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// usageLine is the footer of `template list`: "3 of 10 templates · 4.2 of 50
// GiB", or "3 of 10 templates · 4.2 GiB" on a plan with no storage limit.
func usageLine(u *api.TemplateUsage) string {
	tpl := "templates"
	if u.MaxTemplates == 1 {
		tpl = "template"
	}
	line := fmt.Sprintf("%d of %d %s", u.Templates, u.MaxTemplates, tpl)
	if u.MaxStorageBytes == nil {
		return line + " · " + gibString(u.StorageBytes) + " GiB"
	}
	return line + " · " + gibString(u.StorageBytes) + " of " + gibString(*u.MaxStorageBytes) + " GiB"
}

// templateUsage asks for the team's template usage; nil from a gateway that
// does not have it (404), so the list still prints.
func (a *app) templateUsage(cl *api.Client) *api.TemplateUsage {
	u, err := cl.TemplateUsage(a.ctx)
	if err != nil {
		return nil
	}
	return u
}

func newTemplateListCmd(a *app) *cobra.Command {
	var all bool
	c := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List your templates (--all: the built-in ones too)",
		Long: `List the templates your team built, with their build status and size.

SIZE is the image storage a template keeps: its current build and the one
before it, which is what counts against your plan's template storage. The
last line is your team's usage against its plan: templates, and template
storage in GiB. Built-in templates never count.

--all also lists the built-in templates every team can launch (base, python,
node, desktop, ...), from the gateway's template catalog, with a SOURCE
column (built-in or custom). Built-in templates are listed when the platform
has them registered and ready; when the list cannot be confirmed, STATUS says
"unverified".`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			if all {
				return a.listAllTemplates(cl)
			}
			ts, err := cl.ListTemplates(a.ctx)
			if err != nil {
				return err
			}
			if a.json() {
				for i := range ts {
					a.emit(viewOfTemplate(&ts[i]))
				}
				return nil
			}
			if len(ts) == 0 {
				fmt.Fprintln(a.stderr, "No custom templates. Build one with `anona-compute template build`;\n`anona-compute template list --all` also shows the built-in templates every team can use.")
				return nil
			}
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tTEMPLATE ID\tSTATUS\tCPU\tMEMORY\tDISK\tSIZE\tBUILDS\tUPDATED")
			for _, t := range ts {
				st := t.BuildStatus
				if t.Phase != "" && t.Phase != t.BuildStatus {
					st += " (" + t.Phase + ")"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d MiB\t%d MiB\t%s\t%d\t%s\n", strings.Join(t.Names, ","), t.TemplateID, st, t.CPUCount, t.MemoryMB,
					t.DiskSizeMB, formatBytes(t.StorageBytes), t.BuildCount, t.UpdatedAt.Local().Format(time.DateTime))
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			if u := a.templateUsage(cl); u != nil {
				fmt.Fprintln(a.stdout)
				fmt.Fprintln(a.stdout, usageLine(u))
			}
			return nil
		},
	}
	c.Flags().BoolVar(&all, "all", false, "also list the built-in templates")
	return c
}

// templateRow is one line of `template list --all`.
type templateRow struct {
	Name        string   `json:"name"`
	TemplateID  string   `json:"template_id"`
	Aliases     []string `json:"aliases,omitempty"`
	Source      string   `json:"source"` // built-in | custom
	Status      string   `json:"status"`
	CPUCount    *int     `json:"cpu_count,omitempty"`
	MemoryMB    *int     `json:"memory_mb,omitempty"`
	Description string   `json:"description,omitempty"`
}

// catalogRows merges the template catalog (built-in and launchable custom
// templates) with the team's own list (custom templates in every build
// state). Either may be missing: the catalog on a gateway older than it,
// /v2/templates when the template builder is off (404).
func catalogRows(cat *api.Catalog, custom []api.Template) []templateRow {
	var rows []templateRow
	builtInStatus := "ready"
	if cat != nil && cat.Engine != nil && !cat.Engine.Verified {
		builtInStatus = "unverified"
	}
	seen := map[string]bool{}
	if cat != nil {
		for _, e := range cat.Templates {
			if !e.Public {
				continue
			}
			rows = append(rows, templateRow{Name: first(e.Label, e.TemplateID), TemplateID: e.TemplateID, Aliases: e.Aliases,
				Source: "built-in", Status: builtInStatus, CPUCount: e.CPUCount, MemoryMB: e.MemoryMB, Description: e.Description})
		}
	}
	for i := range custom {
		t := &custom[i]
		seen[t.TemplateID] = true
		st := t.BuildStatus
		if t.Phase != "" && t.Phase != t.BuildStatus {
			st += " (" + t.Phase + ")"
		}
		cpu, mem := t.CPUCount, t.MemoryMB
		name := t.TemplateID
		if len(t.Names) > 0 {
			name = t.Names[0]
		}
		rows = append(rows, templateRow{Name: name, TemplateID: t.TemplateID, Aliases: t.Names, Source: "custom", Status: st,
			CPUCount: &cpu, MemoryMB: &mem})
	}
	if cat != nil {
		for _, e := range cat.Templates {
			if e.Public || seen[e.TemplateID] {
				continue
			}
			rows = append(rows, templateRow{Name: first(e.Label, e.TemplateID), TemplateID: e.TemplateID, Aliases: e.Aliases,
				Source: "custom", Status: builtInStatus, CPUCount: e.CPUCount, MemoryMB: e.MemoryMB})
		}
	}
	return rows
}

// templateCatalog asks for the catalog; a gateway without it answers 404.
func (a *app) templateCatalog(cl *api.Client) (*api.Catalog, error) {
	cat, err := cl.TemplateCatalog(a.ctx)
	if api.StatusOf(err) == http.StatusNotFound {
		return nil, nil
	}
	return cat, err
}

func (a *app) listAllTemplates(cl *api.Client) error {
	cat, err := a.templateCatalog(cl)
	if err != nil {
		return fmt.Errorf("template catalog: %w", err)
	}
	custom, err := cl.ListTemplates(a.ctx)
	if api.StatusOf(err) == http.StatusNotFound {
		custom, err = nil, nil // the template builder is off on this gateway
	}
	if err != nil {
		return err
	}
	rows := catalogRows(cat, custom)
	if a.json() {
		for _, r := range rows {
			a.emit(r)
		}
		return nil
	}
	if cat == nil {
		a.logf("anona-compute: this gateway has no template catalog (GET /v1/template-catalog), so built-in templates cannot be listed\n")
	} else if cat.Engine != nil && !cat.Engine.Verified {
		a.logf("anona-compute: the engine is %s, so this list is not yet confirmed against it; it starts when a sandbox is created\n", cat.Engine.State)
	}
	if len(rows) == 0 {
		fmt.Fprintln(a.stderr, "No templates.")
		return nil
	}
	size := func(p *int, unit string) string {
		if p == nil {
			return "-"
		}
		return strconv.Itoa(*p) + unit
	}
	tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tSTATUS\tTEMPLATE ID\tCPU\tMEMORY\tDESCRIPTION")
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Source, r.Status, r.TemplateID, size(r.CPUCount, ""), size(r.MemoryMB, " MiB"), r.Description)
	}
	return tw.Flush()
}

func newTemplateInfoCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "info NAME|TEMPLATE_ID",
		Short: "Show a template and its builds",
		Args:  exactArgs(1, "NAME|TEMPLATE_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			t, err := cl.GetTemplate(a.ctx, args[0])
			if err != nil {
				return err
			}
			// The sizes it can run at come from the catalog, which knows the
			// caller's plan. Best effort: a template info without them is
			// still worth printing.
			var entry *api.CatalogEntry
			if cat, err := cl.TemplateCatalog(a.ctx); err == nil {
				for i := range cat.Templates {
					if cat.Templates[i].TemplateID == t.TemplateID {
						entry = &cat.Templates[i]
						break
					}
				}
			}
			if a.json() {
				out := map[string]any{"template": viewOfTemplate(&t.Template), "builds": t.Builds}
				if entry != nil {
					out["sizes"] = map[string]any{"minimum": entry.MinimumSize, "default": entry.DefaultSize, "eligible": entry.Sizes}
				}
				a.emit(out)
				return nil
			}
			v := viewOfTemplate(&t.Template)
			tw := tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(tw, "Template ID\t%s\n", v.TemplateID)
			fmt.Fprintf(tw, "Names\t%s\n", strings.Join(v.Names, ", "))
			fmt.Fprintf(tw, "Status\t%s\n", v.Status)
			fmt.Fprintf(tw, "CPU\t%d\n", v.CPUCount)
			fmt.Fprintf(tw, "Memory\t%d MiB\n", v.MemoryMB)
			fmt.Fprintf(tw, "Disk\t%d MiB\n", v.DiskSizeMB)
			if entry != nil && entry.MinimumSize != "" {
				fmt.Fprintf(tw, "Minimum size\t%s (its CPU, memory and disk above are minimums)\n", entry.MinimumSize)
				fmt.Fprintf(tw, "Sizes\t%s\n", sizeList(entry))
			}
			if v.ImageBytes > 0 {
				fmt.Fprintf(tw, "Image\t%s\n", formatBytes(v.ImageBytes))
			}
			if v.StorageBytes > 0 {
				fmt.Fprintf(tw, "Storage\t%s (current and previous build)\n", formatBytes(v.StorageBytes))
			}
			fmt.Fprintf(tw, "envd\t%s\n", v.EnvdVersion)
			_ = tw.Flush()
			if len(t.Builds) > 0 {
				fmt.Fprintln(a.stdout)
				tw = tabwriter.NewWriter(a.stdout, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "BUILD ID\tSTATUS\tPHASE\tSIZE\tCREATED\tERROR")
				for _, b := range t.Builds {
					fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", b.BuildID, b.Status, b.Phase, formatBytes(b.ImageBytes), b.CreatedAt.Local().Format(time.DateTime), b.Error)
				}
				return tw.Flush()
			}
			return nil
		},
	}
}

func newTemplateDeleteCmd(a *app) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:     "delete NAME|TEMPLATE_ID",
		Aliases: []string{"rm"},
		Short:   "Delete a template (running sandboxes keep running)",
		Args:    exactArgs(1, "NAME|TEMPLATE_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				return usageErr("deleting %s cannot be undone: pass --yes", args[0])
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			if err := cl.DeleteTemplate(a.ctx, args[0]); err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]any{"deleted": args[0]})
			} else {
				fmt.Fprintf(a.stdout, "Deleted template %s\n", args[0])
			}
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "confirm the deletion")
	return c
}

func newTemplateLogsCmd(a *app) *cobra.Command {
	var buildID string
	c := &cobra.Command{
		Use:   "logs NAME|TEMPLATE_ID",
		Short: "Print a build's log (the newest build unless --build), following it while it runs",
		Args:  exactArgs(1, "NAME|TEMPLATE_ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			t, err := cl.GetTemplate(a.ctx, args[0])
			if err != nil {
				return err
			}
			if buildID == "" {
				if len(t.Builds) == 0 {
					return &exitError{code: ExitNotFound, err: fmt.Errorf("template %s has no builds", args[0])}
				}
				buildID = t.Builds[0].BuildID
			}
			return a.followBuild(cl, t.TemplateID, buildID, true)
		},
	}
	c.Flags().StringVar(&buildID, "build", "", "build ID (default: the newest)")
	return c
}

// buildWaitLimit bounds how long the CLI follows one build. The gateway ends
// every build itself (ANONA_TEMPLATE_TIMEOUT, 30 minutes by default, and 10
// for the registering phase alone), so this is only reached when the gateway
// fails to; it is twice the gateway's default so that it never cuts short a
// build the gateway would still finish.
var buildWaitLimit = time.Hour

// followBuild streams a build's events; in text mode log lines go to stdout
// when logsToStdout, else stderr. It fails when the build fails, with the
// gateway's reason, and gives up after buildWaitLimit.
func (a *app) followBuild(cl *api.Client, templateID, buildID string, logsToStdout bool) error {
	out := a.stderr
	if logsToStdout {
		out = a.stdout
	}
	ctx, cancel := context.WithTimeout(a.ctx, buildWaitLimit)
	defer cancel()
	phase := ""
	last, err := cl.FollowTemplateBuild(ctx, templateID, buildID, func(ev api.BuildEvent) {
		if a.json() {
			a.emit(ev)
			return
		}
		switch ev.Type {
		case "log":
			fmt.Fprintf(out, "%s %s\n", ev.Timestamp.Local().Format("15:04:05"), ev.Message)
		case "status":
			// A reopened stream repeats the phase it reopened in.
			if ev.Phase != phase {
				phase = ev.Phase
				fmt.Fprintf(a.stderr, "==> %s\n", ev.Phase)
			}
		}
	})
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) && a.ctx.Err() == nil {
			return &exitError{code: ExitError, err: fmt.Errorf("build %s has not finished after %s; it may still be running: follow it with `anona-compute template logs --build %s %s`",
				buildID, buildWaitLimit, buildID, templateID)}
		}
		return err
	}
	if last == nil {
		return errors.New("the build stream ended without a status")
	}
	if last.Status == "error" {
		reason := last.Error
		if reason == "" {
			reason = "the gateway gave no reason; see `anona-compute template logs`"
		}
		return &exitError{code: ExitError, err: fmt.Errorf("build %s failed: %s", buildID, reason)}
	}
	return nil
}

func newTemplateBuildCmd(a *app) *cobra.Command {
	var file, name, startCmd string
	var cpu, memory, disk int
	var noCache, wait bool
	var tags []string
	c := &cobra.Command{
		Use:   "build [CONTEXT_DIR]",
		Short: "Build a template from a Dockerfile",
		Long: `Builds a template from a Dockerfile and a build context (default: the
current directory), streaming the build log. The gateway builds it with
BuildKit in an isolated builder, adds the Anona runtime (envd), pushes it and
registers it; sandboxes then start from it by name:

  anona-compute template build -f Dockerfile --name my-env
  anona-compute create -t my-env

--cpu, --memory and --disk size every sandbox started from the template:

  anona-compute template build --name big --cpu 4 --memory 8192 --disk 20480

The disk defaults to 1024 MiB. Your plan caps it, and a build that asks for
more is refused with the plan's limit before anything is uploaded.

Every FROM image is pinned by digest. EXPOSE 49983 (envd) is refused. The
image's CMD runs at sandbox start only when the last stage declares CMD or
ENTRYPOINT, or with --start-cmd. .dockerignore in the context is honored.`,
		// Not cobra.MaximumNArgs: its error is a plain one, which exits 1
		// rather than the documented 2 for a usage error.
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 1 {
				return usageErr("usage: %s [CONTEXT_DIR] (one context directory, not %d arguments)", cmd.CommandPath(), len(args))
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			// Everything the gateway would refuse is checked before any
			// network call (and before the build context is packed).
			if cmd.Flags().Changed("cpu") && (cpu < minTemplateCPU || cpu > maxTemplateCPU) {
				return usageErr("--cpu must be %d to %d vCPUs, not %d", minTemplateCPU, maxTemplateCPU, cpu)
			}
			if cmd.Flags().Changed("memory") && (memory < minTemplateMemoryMB || memory > maxTemplateMemoryMB) {
				return usageErr("--memory must be %d to %d MiB, not %d", minTemplateMemoryMB, maxTemplateMemoryMB, memory)
			}
			if cmd.Flags().Changed("disk") && (disk < minTemplateDiskMB || disk > maxTemplateDiskMB) {
				return usageErr("--disk must be %d to %d MiB, not %d", minTemplateDiskMB, maxTemplateDiskMB, disk)
			}
			ctxDir := "."
			if len(args) == 1 {
				ctxDir = args[0]
			}
			if fi, err := os.Stat(ctxDir); err != nil {
				if errors.Is(err, fs.ErrNotExist) {
					return usageErr("context directory not found: %s", ctxDir)
				}
				return usageErr("context directory %s: %v", ctxDir, err)
			} else if !fi.IsDir() {
				return usageErr("context directory %s is not a directory (pass the directory, and the Dockerfile with -f)", ctxDir)
			}
			explicitFile := file != ""
			if !explicitFile {
				file = filepath.Join(ctxDir, "Dockerfile")
			}
			df, err := os.ReadFile(file)
			if err != nil {
				switch {
				case errors.Is(err, fs.ErrNotExist) && !explicitFile:
					return usageErr("no Dockerfile in %s (pass one with -f)", ctxDir)
				case errors.Is(err, fs.ErrNotExist):
					return usageErr("Dockerfile not found: %s", file)
				}
				return usageErr("read Dockerfile: %v", err)
			}
			if name == "" {
				abs, _ := filepath.Abs(ctxDir)
				name = strings.ToLower(filepath.Base(abs))
			}
			cl, _, err := a.client()
			if err != nil {
				return err
			}
			tarPath, hash, size, err := tarContext(ctxDir, file)
			if err != nil {
				return err
			}
			defer os.Remove(tarPath)
			req, err := cl.RequestTemplateBuild(a.ctx, api.TemplateBuildRequest{Name: name, Tags: tags, CPUCount: cpu, MemoryMB: memory}.WithDisk(disk))
			if err != nil {
				return err
			}
			a.logf("Template %s (%s), build %s\n", name, req.TemplateID, req.BuildID)
			if req.CPUCount > 0 {
				a.logf("Size: %d vCPU, %d MiB memory, %d MiB disk\n", req.CPUCount, req.MemoryMB, req.DiskSizeMB)
			}
			present, link, err := cl.TemplateFileLink(a.ctx, req.TemplateID, hash)
			if err != nil {
				return err
			}
			if present {
				a.logf("Build context unchanged (%s), not uploaded again\n", humanBytes(size))
			} else {
				f, err := os.Open(tarPath)
				if err != nil {
					return err
				}
				err = cl.UploadTemplateFile(a.ctx, link, f, size)
				f.Close()
				if err != nil {
					return fmt.Errorf("upload build context: %w", err)
				}
				a.logf("Uploaded build context (%s)\n", humanBytes(size))
			}
			if err := cl.StartDockerfileBuild(a.ctx, req.TemplateID, req.BuildID, api.DockerfileBuild{Dockerfile: string(df), ContextHash: hash,
				StartCmd: startCmd, NoCache: noCache}); err != nil {
				return err
			}
			if !wait {
				if a.json() {
					out := map[string]any{"template_id": req.TemplateID, "build_id": req.BuildID, "name": name, "status": "building"}
					if req.CPUCount > 0 {
						out["cpu_count"], out["memory_mb"], out["disk_size_mb"] = req.CPUCount, req.MemoryMB, req.DiskSizeMB
					}
					a.emit(out)
				} else {
					fmt.Fprintf(a.stdout, "Template ID: %s\nBuild ID: %s\n", req.TemplateID, req.BuildID)
					a.logf("Follow it with `anona-compute template logs %s`\n", name)
				}
				return nil
			}
			if err := a.followBuild(cl, req.TemplateID, req.BuildID, false); err != nil {
				return err
			}
			if !a.json() {
				fmt.Fprintf(a.stdout, "Template %s is ready: anona-compute create -t %s\n", name, name)
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVarP(&file, "file", "f", "", "Dockerfile (default CONTEXT_DIR/Dockerfile)")
	f.StringVarP(&name, "name", "n", "", "template name (default: the context directory's name)")
	f.StringArrayVar(&tags, "tag", nil, "tag for this build (repeatable)")
	f.IntVar(&cpu, "cpu", 0, "minimum vCPUs sandboxes from this template need, 1-8 (default 2); a sandbox can use any size that meets it")
	f.IntVar(&memory, "memory", 0, "minimum memory sandboxes from this template need, 128-16384 MiB (default 2048)")
	f.IntVar(&disk, "disk", 0, "minimum writable disk sandboxes from this template need, 1024-81920 MiB, up to your plan's limit (default 1024)")
	f.StringVar(&startCmd, "start-cmd", "", "command to run when a sandbox starts (after envd)")
	f.BoolVar(&noCache, "no-cache", false, "build without the build cache")
	f.BoolVar(&wait, "wait", true, "stream the build log and wait until it is ready (--wait=false returns at once)")
	return c
}

// Template size limits, checked before anything is uploaded. The gateway
// enforces cpuCount 1..MaxCPU and memoryMB 128..MaxMemoryMB with MaxCPU 8
// and MaxMemoryMB 16384, and diskSizeMB MinDiskSizeMB..MaxDiskSizeMB,
// lowered by the plan's per-sandbox disk limit (checked there, not here).
const (
	minTemplateCPU      = 1
	maxTemplateCPU      = 8
	minTemplateMemoryMB = 128
	maxTemplateMemoryMB = 16384
	minTemplateDiskMB   = 1024
	maxTemplateDiskMB   = 81920 // the XL sandbox size's disk
)

func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

// readIgnore reads .dockerignore patterns (no "!" exceptions).
func readIgnore(dir string) []string {
	f, err := os.Open(filepath.Join(dir, ".dockerignore"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || strings.HasPrefix(l, "#") || strings.HasPrefix(l, "!") {
			continue
		}
		out = append(out, strings.TrimPrefix(path.Clean(filepath.ToSlash(l)), "/"))
	}
	return out
}

func ignored(rel string, pats []string) bool {
	for _, p := range pats {
		for cur := rel; cur != "." && cur != "/"; cur = path.Dir(cur) {
			if ok, _ := path.Match(p, cur); ok {
				return true
			}
		}
	}
	return false
}

// tarContext writes the build context as a gzipped tar with fixed times and
// owners, so an unchanged context hashes the same and is not uploaded twice.
func tarContext(dir, dockerfile string) (string, string, int64, error) {
	pats := readIgnore(dir)
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if ignored(rel, pats) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		return "", "", 0, err
	}
	sort.Strings(files)
	tmp, err := os.CreateTemp("", "anona-context-*.tar.gz")
	if err != nil {
		return "", "", 0, err
	}
	h := sha256.New()
	gz := gzip.NewWriter(io.MultiWriter(tmp, h))
	gz.ModTime = time.Time{}
	tw := tar.NewWriter(gz)
	for _, rel := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		fi, err := os.Lstat(p)
		if err != nil {
			return "", "", 0, err
		}
		link := ""
		if fi.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return "", "", 0, err
			}
		}
		hdr, err := tar.FileInfoHeader(fi, link)
		if err != nil {
			continue // sockets and devices are skipped
		}
		hdr.Name = rel
		if fi.IsDir() {
			hdr.Name += "/"
		}
		hdr.ModTime, hdr.AccessTime, hdr.ChangeTime = time.Unix(0, 0), time.Time{}, time.Time{}
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		hdr.Format = tar.FormatPAX
		if err := tw.WriteHeader(hdr); err != nil {
			return "", "", 0, err
		}
		if fi.Mode().IsRegular() {
			f, err := os.Open(p)
			if err != nil {
				return "", "", 0, err
			}
			_, err = io.Copy(tw, f)
			f.Close()
			if err != nil {
				return "", "", 0, err
			}
		}
	}
	if err := tw.Close(); err != nil {
		return "", "", 0, err
	}
	if err := gz.Close(); err != nil {
		return "", "", 0, err
	}
	size, _ := tmp.Seek(0, io.SeekCurrent)
	if err := tmp.Close(); err != nil {
		return "", "", 0, err
	}
	_ = dockerfile
	return tmp.Name(), hex.EncodeToString(h.Sum(nil)), size, nil
}

// sizeList is the sizes a template may be created at, the default marked.
func sizeList(e *api.CatalogEntry) string {
	if len(e.Sizes) == 0 {
		return "none on your plan"
	}
	parts := make([]string, 0, len(e.Sizes))
	for _, o := range e.Sizes {
		p := fmt.Sprintf("%s (%d vCPU, %d GiB, %d GB disk)", o.Name, o.CPUCount, o.MemoryMB/1024, o.DiskSizeMB/1024)
		if o.Name == e.DefaultSize {
			p += " default"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}
