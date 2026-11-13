package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/config"
)

func newVersionCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the CLI version",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if a.json() {
				a.emit(map[string]string{"version": a.version})
				return nil
			}
			fmt.Fprintln(a.stdout, a.version)
			return nil
		},
	}
}

func newConfigCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Read and write profile settings",
		Long: `Read and write settings of the selected profile (--profile, default "default").

Keys: ` + strings.Join(config.Keys, ", ") + `. The file is written with mode 0600.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	profileName := func() string {
		if a.profile != "" {
			return a.profile
		}
		if p := a.getenv("ANONA_COMPUTE_PROFILE"); p != "" {
			return p
		}
		return config.DefaultProfile
	}
	var reveal bool
	get := &cobra.Command{
		Use:   "get KEY",
		Short: "Print one setting (api_key is masked unless --reveal)",
		Args:  exactArgs(1, "KEY"),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := a.configPath()
			if err != nil {
				return err
			}
			f, err := config.Load(path)
			if err != nil {
				return err
			}
			v, err := f.Profiles[profileName()].Get(args[0])
			if err != nil {
				return &exitError{code: ExitUsage, err: err}
			}
			if args[0] == "api_key" && !reveal {
				v = maskKey(v)
			}
			if a.json() {
				a.emit(map[string]string{"profile": profileName(), "key": args[0], "value": v})
				return nil
			}
			fmt.Fprintln(a.stdout, v)
			return nil
		},
	}
	get.Flags().BoolVar(&reveal, "reveal", false, "print the API key itself instead of a masked one")
	c.AddCommand(get)
	c.AddCommand(&cobra.Command{
		Use:   "set KEY VALUE",
		Short: "Change one setting",
		Args:  exactArgs(2, "KEY VALUE"),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := a.configPath()
			if err != nil {
				return err
			}
			f, err := config.Load(path)
			if err != nil {
				return err
			}
			if args[0] == "api_key" && !savedKeyRE.MatchString(args[1]) {
				return usageErr("%s", savedKeyFormatMsg)
			}
			p := f.Profiles[profileName()]
			if err := p.Set(args[0], args[1]); err != nil {
				return &exitError{code: ExitUsage, err: err}
			}
			f.Profiles[profileName()] = p
			if err := config.Save(path, f); err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]string{"profile": profileName(), "key": args[0], "status": "set"})
			}
			return nil
		},
	})
	c.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List every profile and its settings (API keys are masked)",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := a.configPath()
			if err != nil {
				return err
			}
			f, err := config.Load(path)
			if err != nil {
				return err
			}
			for _, name := range f.ProfileNames() {
				p := f.Profiles[name]
				p.APIKey = maskKey(p.APIKey)
				if a.json() {
					a.emit(map[string]any{"profile": name, "api_url": p.APIURL, "api_key": p.APIKey, "domain": p.Domain, "default_template": p.DefaultTemplate})
					continue
				}
				fmt.Fprintf(a.stdout, "[%s]\n", name)
				for _, k := range config.Keys {
					v, _ := p.Get(k)
					if v != "" {
						fmt.Fprintf(a.stdout, "  %s = %s\n", k, v)
					}
				}
			}
			return nil
		},
	})
	return c
}

// maskKey hides all of a key but its ac_/e2b_ prefix and last four characters,
// the part the dashboard shows too.
func maskKey(k string) string {
	if len(k) <= 8 {
		return strings.Repeat("*", len(k))
	}
	n := 4
	if strings.HasPrefix(k, "ac_") {
		n = 3
	}
	return k[:n] + strings.Repeat("*", len(k)-n-4) + k[len(k)-4:]
}

type notBuiltSpec struct {
	use, short, needs string
}

// Commands from the plan whose server side does not exist yet. They print a
// clear message and exit 1 instead of pretending to work.
var notBuilt = []notBuiltSpec{
	{"keys", "Manage API keys", "the gateway API keys endpoint"},
	{"limits", "Show plan limits and usage", "the quotas endpoint"},
	{"push", "Push a git working tree into a sandbox", "file upload through envd"},
	{"recover", "Recover a sandbox from its last snapshot", "snapshot recovery in the gateway"},
}

func newNotBuiltCmd(nb notBuiltSpec) *cobra.Command {
	return &cobra.Command{
		Use:                nb.use,
		Short:              nb.short + " (not available yet)",
		Args:               cobra.ArbitraryArgs,
		FParseErrWhitelist: cobra.FParseErrWhitelist{UnknownFlags: true},
		RunE: func(cmd *cobra.Command, args []string) error {
			return &exitError{code: ExitError, err: fmt.Errorf("`anona-compute %s` is not available yet: it needs %s", nb.use, nb.needs)}
		},
	}
}
