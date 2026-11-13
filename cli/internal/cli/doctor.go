package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/auth"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/config"
)

// doctorCheck is one line of `anona-compute doctor`.
type doctorCheck struct {
	Check     string `json:"check"`
	Status    string `json:"status"` // ok | warn | fail | skip
	Detail    string `json:"detail"`
	LatencyMS *int64 `json:"latency_ms,omitempty"`
}

// credentialSource names where the credential in use comes from, in the
// order config.Resolve picks it.
func (a *app) credentialSource(s config.Settings, p config.Profile) (string, bool) {
	switch {
	case a.apiKey != "":
		return "API key from --api-key " + maskKey(s.APIKey), true
	case a.getenv("ANONA_COMPUTE_API_KEY") != "":
		return "API key from ANONA_COMPUTE_API_KEY " + maskKey(s.APIKey), true
	case (strings.HasPrefix(a.getenv("ANONA_API_KEY"), "e2b_") || strings.HasPrefix(a.getenv("ANONA_API_KEY"), "ac_")):
		return "API key from ANONA_API_KEY " + maskKey(s.APIKey), true
	case a.getenv("E2B_API_KEY") != "":
		return "API key from E2B_API_KEY " + maskKey(s.APIKey), true
	case p.APIKey != "":
		return "API key from the config file " + maskKey(s.APIKey), true
	case s.Session:
		where := "the OS keychain"
		if s.CredentialStore == auth.StoreFile {
			where = "credentials.json"
		}
		who := s.Email
		if s.Org != "" {
			who += " in " + s.Org
		}
		return "browser login (" + strings.TrimSpace(who) + ", session in " + where + ")", true
	}
	return "no credential", false
}

func newDoctorCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check the configuration, credentials and connection to Anona Compute",
		Long: `Check, one line each, everything a command needs, and say what is wrong:

  config     which profile and config file are used, and where the credential
             comes from (--api-key, ANONA_COMPUTE_API_KEY or E2B_API_KEY, the
             config file, or a browser login in the keychain or credentials.json)
  api        the gateway answers at the API URL (GET /healthz), and how fast
  auth       the gateway accepts the credential (as whoami checks it)
  templates  the built-in templates can be listed from the template catalog
  ssh        the ssh binary is on PATH (needed by ssh, scp and the MCP ssh tool)

Each check is ok, warn or fail; a check that cannot run because an earlier
one failed is skip. Exit 0 when nothing failed, 1 otherwise. --json prints
one object per check: {"check","status","detail","latency_ms"?}.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			checks := a.doctor()
			failed := false
			for _, c := range checks {
				if c.Status == "fail" {
					failed = true
				}
			}
			if a.json() {
				for _, c := range checks {
					a.emit(c)
				}
			} else {
				for _, c := range checks {
					detail := c.Detail
					if c.LatencyMS != nil {
						detail += fmt.Sprintf(" (%d ms)", *c.LatencyMS)
					}
					fmt.Fprintf(a.stdout, "%-5s %-10s %s\n", c.Status, c.Check, detail)
				}
			}
			if failed {
				return silentExit(ExitError)
			}
			return nil
		},
	}
}

func (a *app) doctor() []doctorCheck {
	var out []doctorCheck
	add := func(c doctorCheck) { out = append(out, c) }
	skip := func(names ...string) {
		for _, n := range names {
			add(doctorCheck{Check: n, Status: "skip", Detail: "not checked: an earlier check failed"})
		}
	}

	// config
	path, err := a.configPath()
	if err != nil {
		add(doctorCheck{Check: "config", Status: "fail", Detail: err.Error()})
		skip("api", "auth", "templates")
		return append(out, a.sshCheck())
	}
	f, err := config.Load(path)
	if err != nil {
		add(doctorCheck{Check: "config", Status: "fail", Detail: fmt.Sprintf("%s: %v", path, err)})
		skip("api", "auth", "templates")
		return append(out, a.sshCheck())
	}
	s, _ := a.settings()
	p, inFile := f.Profiles[s.Profile]
	src, haveCred := a.credentialSource(s, p)
	where := path
	if !inFile {
		where = path + ", which has no such profile (defaults apply)"
	}
	detail := fmt.Sprintf("profile %s from %s; %s; api %s", s.Profile, where, src, s.APIURL)
	switch {
	case !haveCred:
		add(doctorCheck{Check: "config", Status: "fail", Detail: detail + ": run `anona-compute login`, or set ANONA_COMPUTE_API_KEY"})
	case s.APIKey != "" && !apiKeyRE.MatchString(s.APIKey):
		add(doctorCheck{Check: "config", Status: "fail", Detail: detail + ": the API key is not ac_ (or e2b_) followed by lowercase hex"})
		haveCred = false
	default:
		add(doctorCheck{Check: "config", Status: "ok", Detail: detail})
	}

	// api: /healthz is on the gateway's public listener and needs no
	// credential. (/readyz and /enginez are on the admin listener, port
	// 8090, which is not reachable from outside.)
	hc := &http.Client{Timeout: 10 * time.Second}
	if a.reqTimeout > 0 {
		hc.Timeout = a.reqTimeout
	}
	base := strings.TrimRight(s.APIURL, "/")
	start := time.Now()
	resp, err := a.get(hc, base+"/healthz")
	ms := time.Since(start).Milliseconds()
	if err != nil {
		add(doctorCheck{Check: "api", Status: "fail", Detail: fmt.Sprintf("%s does not answer: %v", s.APIURL, err)})
		skip("auth", "templates")
		return append(out, a.sshCheck())
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		add(doctorCheck{Check: "api", Status: "fail", Detail: fmt.Sprintf("GET %s/healthz answered HTTP %d: is the API URL right?", base, resp.StatusCode), LatencyMS: &ms})
		skip("auth", "templates")
		return append(out, a.sshCheck())
	}
	apiDetail := s.APIURL + " is up"
	if v := a.gatewayVersion(hc, base); v != "" {
		apiDetail += ", gateway " + v
	}
	apiDetail += ", CLI " + a.version
	add(doctorCheck{Check: "api", Status: "ok", Detail: apiDetail, LatencyMS: &ms})

	// auth
	if !haveCred {
		skip("auth", "templates")
		return append(out, a.sshCheck())
	}
	cl, _, err := a.client()
	if err == nil {
		err = a.checkAuth(cl)
	}
	if err != nil {
		hint := ""
		if exitCodeFor(err) == ExitAuth {
			hint = ": sign in again with `anona-compute login`, or check the API key"
		}
		add(doctorCheck{Check: "auth", Status: "fail", Detail: "the gateway refused the credential: " + err.Error() + hint})
		skip("templates")
		return append(out, a.sshCheck())
	}
	add(doctorCheck{Check: "auth", Status: "ok", Detail: "the gateway accepts the credential"})

	// templates
	cat, err := a.templateCatalog(cl)
	switch {
	case err != nil:
		add(doctorCheck{Check: "templates", Status: "fail", Detail: "the template catalog failed: " + err.Error()})
	case cat == nil:
		add(doctorCheck{Check: "templates", Status: "warn", Detail: "this gateway has no template catalog, so built-in templates cannot be listed"})
	default:
		var names []string
		for _, e := range cat.Templates {
			if e.Public {
				names = append(names, first(e.Label, e.TemplateID))
			}
		}
		switch {
		case len(names) == 0:
			add(doctorCheck{Check: "templates", Status: "fail", Detail: "no built-in template is available"})
		case cat.Engine != nil && !cat.Engine.Verified:
			add(doctorCheck{Check: "templates", Status: "ok", Detail: fmt.Sprintf("%d built-in: %s (the engine is %s and starts on the next create, so not yet confirmed)",
				len(names), strings.Join(names, ", "), cat.Engine.State)})
		default:
			add(doctorCheck{Check: "templates", Status: "ok", Detail: fmt.Sprintf("%d built-in: %s", len(names), strings.Join(names, ", "))})
		}
	}
	return append(out, a.sshCheck())
}

func (a *app) get(hc *http.Client, u string) (*http.Response, error) {
	ctx, cancel := context.WithTimeout(a.ctx, hc.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", a.userAgent())
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	// Read the (small) body before the context is cancelled.
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	resp.Body = io.NopCloser(strings.NewReader(string(body)))
	return resp, nil
}

// gatewayVersion reads the public GET /version, if the gateway has it.
func (a *app) gatewayVersion(hc *http.Client, base string) string {
	resp, err := a.get(hc, base+"/version")
	if err != nil || resp.StatusCode != http.StatusOK {
		return ""
	}
	var v struct {
		Version string `json:"version"`
	}
	if json.NewDecoder(resp.Body).Decode(&v) != nil {
		return ""
	}
	return v.Version
}

// checkAuth proves the credential the way whoami does.
func (a *app) checkAuth(cl *api.Client) error {
	if a.tokens != nil {
		_, err := a.tokens.Current(a.ctx)
		if errors.Is(err, auth.ErrSessionRevoked) {
			return &exitError{code: ExitAuth, err: err}
		}
		return err
	}
	_, _, err := cl.ListSandboxes(a.ctx, apiListOne)
	if api.StatusOf(err) == http.StatusForbidden {
		return nil // a valid key without the sandboxes scope
	}
	return err
}

func (a *app) sshCheck() doctorCheck {
	if p, err := exec.LookPath("ssh"); err == nil {
		return doctorCheck{Check: "ssh", Status: "ok", Detail: p}
	}
	return doctorCheck{Check: "ssh", Status: "warn", Detail: "ssh is not on PATH: anona-compute ssh and scp need OpenSSH (exec and connect do not)"}
}
