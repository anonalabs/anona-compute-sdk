package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/auth"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/config"
)

var apiListOne = api.ListParams{Limit: 1}

// newSandboxCmd groups the sandbox commands as `anona-compute sandbox <cmd>`, the
// layout of the E2B CLI (`e2b sandbox list`). The top-level forms stay.
func newSandboxCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:     "sandbox",
		Aliases: []string{"sbx"},
		Short:   "Sandbox commands (same as the top-level create, list, info, ...)",
		Args:    noArgs,
		RunE:    func(cmd *cobra.Command, args []string) error { return cmd.Help() },
	}
	c.AddCommand(newCreateCmd(a), newListCmd(a), newInfoCmd(a), newKillCmd(a), newPauseCmd(a),
		newResumeCmd(a), newTimeoutCmd(a), newRenameCmd(a), newForkCmd(a), newExecCmd(a), newOutputCmd(a), newPsCmd(a), newLogsCmd(a))
	return c
}

func (a *app) profileName() string {
	if a.profile != "" {
		return a.profile
	}
	if p := a.getenv("ANONA_COMPUTE_PROFILE"); p != "" {
		return p
	}
	return config.DefaultProfile
}

// forgetSession removes a profile's browser-login state. With revoke, the
// session is first deleted on the dashboard (best effort when revoke is
// "try"). It returns whether the dashboard confirmed a revocation.
func (a *app) forgetSession(s config.Settings, p *config.Profile, revoke bool) (revoked bool, err error) {
	if p.Auth != config.AuthSession {
		return false, nil
	}
	st, err := a.credStore()
	if err != nil {
		return false, err
	}
	secret, lerr := st.Load(p.CredentialStore, s.Profile)
	if revoke && lerr == nil {
		ctx, cancel := context.WithTimeout(a.ctx, 15*time.Second)
		revoked, err = a.dashboard(s).Logout(ctx, secret)
		cancel()
		if err != nil {
			return false, fmt.Errorf("revoke the session on %s: %w (nothing was removed; retry, or delete it locally with `anona-compute logout --local`)", s.WebURL, err)
		}
	}
	if err := st.Delete(p.CredentialStore, s.Profile); err != nil {
		return revoked, err
	}
	p.Auth, p.CredentialStore, p.Org, p.Email = "", "", "", ""
	return revoked, nil
}

func newLoginCmd(a *app) *cobra.Command {
	var withKey, noBrowser, noVerify bool
	var org string
	c := &cobra.Command{
		Use:   "login",
		Short: "Sign in through the browser (or save an API key with --with-api-key)",
		Long: `Sign in to Anona Compute.

Without flags, anona-compute login starts a device sign-in (RFC 8628): it prints a
URL and a code, opens the browser, and waits while you sign in to the
dashboard, check the code, pick the organization and approve. The CLI then
holds a dashboard session for this machine and trades it for gateway tokens
that last 15 minutes. The session secret is stored in the OS keychain when
one is available, else in credentials.json (mode 0600) next to config.toml;
ANONA_COMPUTE_CREDENTIAL_STORE=file|keyring forces one. anona-compute logout revokes it.

--with-api-key reads an API key (ac_ + 40 lowercase hex characters) from
stdin, saves it to the profile, and checks it against the gateway (an
invalid key is still saved, and the command exits 3; --no-verify skips the
check).

The dashboard is --web-url, else ANONA_COMPUTE_WEB_URL, else the profile's web_url,
else https://app.<domain>.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if withKey {
				return a.loginWithKey(!noVerify)
			}
			return a.loginDevice(org, noBrowser)
		},
	}
	f := c.Flags()
	f.BoolVar(&withKey, "with-api-key", false, "read an API key from stdin instead of signing in through the browser")
	f.BoolVar(&noBrowser, "no-browser", false, "print the sign-in URL but do not open a browser")
	f.BoolVar(&noVerify, "no-verify", false, "with --with-api-key: save the key without checking it against the gateway")
	f.StringVar(&org, "org", "", "organization slug to act in (default: the one picked in the browser)")
	f.StringVar(&a.webURL, "web-url", "", "dashboard URL (env ANONA_COMPUTE_WEB_URL; default https://app.<domain>)")
	return c
}

func (a *app) loginWithKey(verify bool) error {
	line, err := bufio.NewReader(a.stdin).ReadString('\n')
	if err != nil && line == "" {
		return usageErr("no API key on stdin")
	}
	key := strings.TrimSpace(line)
	if !savedKeyRE.MatchString(key) {
		return usageErr("%s", savedKeyFormatMsg)
	}
	path, err := a.configPath()
	if err != nil {
		return err
	}
	f, err := config.Load(path)
	if err != nil {
		return err
	}
	s, err := a.settings()
	if err != nil {
		return err
	}
	name := a.profileName()
	p := f.Profiles[name]
	// Replacing a browser login: revoke it if the dashboard is reachable.
	if p.Auth == config.AuthSession {
		if _, err := a.forgetSession(s, &p, true); err != nil {
			a.logf("anona-compute: %v\n", err)
			_, _ = a.forgetSession(s, &p, false)
		}
	}
	p.APIKey = key
	if a.apiURL != "" {
		p.APIURL = a.apiURL
	}
	f.Profiles[name] = p
	if err := config.Save(path, f); err != nil {
		return err
	}
	if env := a.envKeyName(); env != "" {
		a.logf("anona-compute: note: %s is set in the environment and is used instead of the saved key\n", env)
	}
	if !verify {
		if a.json() {
			a.emit(map[string]any{"profile": name, "status": "saved", "verified": false})
			return nil
		}
		fmt.Fprintf(a.stderr, "Saved API key to profile %q in %s (not verified)\n", name, path)
		return nil
	}
	// s was resolved before the save, from the same flags, environment and
	// profile: its API URL is the one this key will be used with.
	team, verr := a.verifyKey(s.APIURL, key)
	out := map[string]any{"profile": name, "status": "saved", "verified": verr == nil}
	switch st := api.StatusOf(verr); {
	case verr == nil:
		if team != "" {
			out["team"] = team
		}
		if a.json() {
			a.emit(out)
			return nil
		}
		if team != "" {
			fmt.Fprintf(a.stderr, "Saved and verified API key for %s (profile %q in %s)\n", team, name, path)
		} else {
			fmt.Fprintf(a.stderr, "Saved and verified API key (profile %q in %s)\n", name, path)
		}
		return nil
	case st == http.StatusUnauthorized:
		out["status_code"] = st
		if a.json() {
			a.emit(out)
		} else {
			fmt.Fprintf(a.stderr, "Saved API key to profile %q, but the gateway rejected it (%d)\n", name, st)
		}
		return silentExit(ExitAuth)
	default:
		out["verify_error"] = verr.Error()
		if a.json() {
			a.emit(out)
			return nil
		}
		fmt.Fprintf(a.stderr, "Saved API key to profile %q in %s, but could not verify it: %v\n", name, path, verr)
		return nil
	}
}

// envKeyName names the environment variable whose key overrides a saved one.
func (a *app) envKeyName() string {
	for _, k := range []string{"ANONA_COMPUTE_API_KEY", "E2B_API_KEY"} {
		if a.getenv(k) != "" {
			return k
		}
	}
	return ""
}

// verifyKey proves a key against the gateway, the way whoami does (the
// gateway has no account endpoint for keys: a one-item list is the cheapest
// call every key may make), and names its team when the key may read usage.
func (a *app) verifyKey(apiURL, key string) (team string, err error) {
	hc := &http.Client{Timeout: 15 * time.Second}
	if a.reqTimeout > 0 {
		hc.Timeout = a.reqTimeout
	}
	cl := &api.Client{BaseURL: apiURL, APIKey: key, UserAgent: a.userAgent(), HTTP: hc}
	if _, _, err := cl.ListSandboxes(a.ctx, apiListOne); err != nil {
		if api.StatusOf(err) == http.StatusForbidden {
			// The key is real: it only lacks the scope to list sandboxes.
			return "", nil
		}
		return "", err
	}
	if u, err := cl.Usage(a.ctx, "current"); err == nil {
		team = first(u.Tenant.Name, u.Tenant.Slug)
	}
	return team, nil
}

// openBrowser starts the platform's URL opener; it does not wait for it.
func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return errors.New("no display")
		}
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func (a *app) loginDevice(org string, noBrowser bool) error {
	s, err := a.settings()
	if err != nil {
		return err
	}
	dash := a.dashboard(s)
	dc, err := dash.RequestCode(a.ctx)
	if err != nil {
		return err
	}
	if a.json() {
		a.emit(map[string]any{"type": "device_code", "verification_uri": dc.VerificationURI,
			"verification_uri_complete": dc.VerificationURIComplete, "user_code": dc.FormattedCode(), "expires_in": dc.ExpiresIn})
	} else {
		fmt.Fprintf(a.stderr, "To sign in, open this page and check that it shows the code below:\n\n  %s\n\n  Code: %s\n\n",
			dc.VerificationURIComplete, dc.FormattedCode())
	}
	if !noBrowser && a.getenv("ANONA_COMPUTE_NO_BROWSER") == "" {
		if err := openBrowser(dc.VerificationURIComplete); err != nil {
			a.logf("Could not open a browser (%v); open the URL yourself.\n", err)
		} else {
			a.logf("Opened your browser.\n")
		}
	}
	a.logf("Waiting for approval (Ctrl-C to cancel)...\n")
	tok, err := dash.Poll(a.ctx, dc)
	if err != nil {
		return &exitError{code: ExitAuth, err: err}
	}

	ex, err := dash.ExchangeToken(a.ctx, tok.AccessToken, org)
	var oreq *auth.OrgRequiredError
	if errors.As(err, &oreq) {
		org, err = a.pickOrg(oreq.Orgs)
		if err == nil {
			ex, err = dash.ExchangeToken(a.ctx, tok.AccessToken, org)
		}
	}
	if err != nil {
		// Do not leave an unusable session behind.
		_, _ = dash.Logout(context.Background(), tok.AccessToken)
		var ee *exitError
		if errors.As(err, &ee) {
			return err
		}
		return &exitError{code: ExitAuth, err: err}
	}

	path, err := a.configPath()
	if err != nil {
		return err
	}
	f, err := config.Load(path)
	if err != nil {
		return err
	}
	name := a.profileName()
	p := f.Profiles[name]
	if p.Auth == config.AuthSession {
		// An earlier login on this profile: revoke that session.
		if _, err := a.forgetSession(s, &p, true); err != nil {
			a.verbosef("previous session: %v", err)
			_, _ = a.forgetSession(s, &p, false)
		}
	}
	cs, err := a.credStore()
	if err != nil {
		return err
	}
	used, err := cs.Save(name, tok.AccessToken)
	if err != nil {
		_, _ = dash.Logout(context.Background(), tok.AccessToken)
		return err
	}
	p.Auth, p.CredentialStore, p.Org, p.Email = config.AuthSession, used, ex.Org.Slug, ex.User.Email
	p.APIKey = ""
	if a.webURL != "" || a.getenv("ANONA_COMPUTE_WEB_URL") != "" {
		p.WebURL = s.WebURL
	}
	if a.apiURL != "" {
		p.APIURL = a.apiURL
	}
	f.Profiles[name] = p
	if err := config.Save(path, f); err != nil {
		return err
	}
	where := "the OS keychain"
	if used == auth.StoreFile {
		where = cs.FilePath + " (mode 0600)"
	}
	if a.json() {
		a.emit(map[string]any{"type": "logged_in", "profile": name, "email": ex.User.Email, "org": ex.Org.Slug,
			"org_name": ex.Org.Name, "role": ex.Role, "credential_store": used})
		return nil
	}
	fmt.Fprintf(a.stderr, "Signed in as %s in organization %s (%s) with role %s.\nSession saved for profile %q in %s.\n",
		ex.User.Email, ex.Org.Name, ex.Org.Slug, ex.Role, name, where)
	return nil
}

// pickOrg asks on the terminal; without one it fails with the choices.
func (a *app) pickOrg(orgs []auth.OrgChoice) (string, error) {
	var names []string
	for _, o := range orgs {
		names = append(names, o.Slug)
	}
	in, ok := a.stdin.(*os.File)
	if !ok || !term.IsTerminal(int(in.Fd())) || a.json() {
		return "", usageErr("you belong to several organizations (%s); run `anona-compute login --org <slug>`", strings.Join(names, ", "))
	}
	fmt.Fprintln(a.stderr, "Pick an organization:")
	for i, o := range orgs {
		fmt.Fprintf(a.stderr, "  %d) %s (%s, %s)\n", i+1, o.Name, o.Slug, o.Role)
	}
	fmt.Fprint(a.stderr, "Number: ")
	line, _ := bufio.NewReader(in).ReadString('\n')
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil || n < 1 || n > len(orgs) {
		return "", usageErr("no organization picked")
	}
	return orgs[n-1].Slug, nil
}

func newLogoutCmd(a *app) *cobra.Command {
	var local bool
	c := &cobra.Command{
		Use:   "logout",
		Short: "Sign out: revoke the CLI session on the dashboard and remove local credentials",
		Long: `Sign out of the selected profile. A browser login's session is deleted on
the dashboard first (gateway tokens already issued from it stop working
within seconds), then removed from the keychain or
credentials file. An API key saved with --with-api-key is removed from the
profile (revoke the key itself in the dashboard).`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := a.configPath()
			if err != nil {
				return err
			}
			f, err := config.Load(path)
			if err != nil {
				return err
			}
			s, err := a.settings()
			if err != nil {
				return err
			}
			name := a.profileName()
			p, ok := f.Profiles[name]
			status := "logged_out"
			if ok {
				hadSession := p.Auth == config.AuthSession
				revoked, err := a.forgetSession(s, &p, !local)
				if err != nil {
					return err
				}
				switch {
				case hadSession && revoked:
					status = "revoked"
					a.logf("Revoked the CLI session on %s and removed it from this machine.\n", s.WebURL)
				case hadSession && !local:
					status = "already_revoked"
					a.logf("The session was already gone on %s; removed it from this machine.\n", s.WebURL)
				case hadSession:
					a.logf("Removed the session from this machine (not revoked on the dashboard).\n")
				}
				if p.APIKey != "" {
					a.logf("Removed the API key from profile %q.\n", name)
				}
				p.APIKey = ""
				f.Profiles[name] = p
				if err := config.Save(path, f); err != nil {
					return err
				}
			}
			if a.json() {
				a.emit(map[string]string{"profile": name, "status": status})
			}
			return nil
		},
	}
	c.Flags().BoolVar(&local, "local", false, "only remove local credentials; do not contact the dashboard")
	return c
}

func newWhoamiCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show who the CLI is signed in as",
		Long: `Show the account behind the current credentials. For a browser login this
asks the dashboard (user, organization, role); for an API key it checks the
key against the gateway.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, s, err := a.client()
			if err != nil {
				return err
			}
			if a.tokens != nil {
				ex, err := a.tokens.Current(a.ctx)
				if errors.Is(err, auth.ErrSessionRevoked) {
					return &exitError{code: ExitAuth, err: err}
				}
				if err != nil {
					return err
				}
				if a.json() {
					a.emit(map[string]any{"auth": "session", "email": ex.User.Email, "user_id": ex.User.ID, "org": ex.Org.Slug,
						"org_id": ex.Org.ID, "org_name": ex.Org.Name, "role": ex.Role, "profile": s.Profile, "web_url": s.WebURL, "api_url": s.APIURL})
					return nil
				}
				fmt.Fprintf(a.stdout, "%s\norganization: %s (%s)\nrole: %s\nprofile: %s (browser login via %s)\napi: %s\n",
					ex.User.Email, ex.Org.Name, ex.Org.Slug, ex.Role, s.Profile, s.WebURL, s.APIURL)
				return nil
			}
			// API key: the gateway has no account endpoint; a list call proves the key.
			if _, _, err := cl.ListSandboxes(a.ctx, apiListOne); err != nil {
				return err
			}
			if a.json() {
				a.emit(map[string]any{"auth": "api_key", "api_key": maskKey(s.APIKey), "profile": s.Profile, "api_url": s.APIURL})
				return nil
			}
			fmt.Fprintf(a.stdout, "API key %s (valid)\nprofile: %s\napi: %s\n", maskKey(s.APIKey), s.Profile, s.APIURL)
			return nil
		},
	}
}
