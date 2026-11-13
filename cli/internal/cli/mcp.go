package cli

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/api"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/config"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/envd"
	"github.com/anonalabs/anona-compute-sdk/cli/internal/mcpserver"
)

func newMCPCmd(a *app) *cobra.Command {
	var httpAddr string
	var readOnly bool
	c := &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server so AI agents can use Anona Compute",
		Long: `Serve the Model Context Protocol so AI agents (Claude Code, Codex, Cursor and
other MCP clients) can create sandboxes, run commands, move files and delete
sandboxes with this CLI's credentials (anona-compute login, or an API key).

By default the server speaks MCP over stdio; the agent starts it:

  claude mcp add anona-compute -- anona-compute mcp
  codex mcp add anona-compute -- anona-compute mcp

--http 127.0.0.1:8765 serves streamable HTTP at http://127.0.0.1:8765/mcp
instead. Only loopback addresses are allowed, and every request must carry
Authorization: Bearer <token>, where the token is ANONA_COMPUTE_MCP_TOKEN or a random
one printed on stderr at start.

Every tool call goes through the gateway with your credential, so it can do
exactly what you can. A viewer (or --read-only) gets the read-only tools only.`,
		Args: noArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if httpAddr != "" {
				if err := checkLoopbackAddr(httpAddr); err != nil {
					return err
				}
			}
			cl, s, err := a.client()
			if err != nil {
				return err
			}
			cfg, err := a.mcpConfig(cl, s, readOnly)
			if err != nil {
				return err
			}
			srv := mcpserver.New(cfg)
			if httpAddr == "" {
				err := srv.Run(a.ctx, &mcp.StdioTransport{})
				if errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}
			return a.serveMCPHTTP(srv, httpAddr)
		},
	}
	c.Flags().StringVar(&httpAddr, "http", "", "serve streamable HTTP on this loopback address (for example 127.0.0.1:8765) instead of stdio")
	c.Flags().BoolVar(&readOnly, "read-only", false, "offer only the tools that do not change sandboxes")
	return c
}

func (a *app) mcpConfig(cl *api.Client, s config.Settings, readOnly bool) (mcpserver.Config, error) {
	cfg := mcpserver.Config{
		API:      cl,
		ReadOnly: readOnly,
		Version:  a.version,
		Envd: func(id, token, version string, domain *string, user string) *envd.Client {
			ec := a.envdClient(s, id, token, version, domain, user)
			ec.HTTP = &http.Client{}
			return ec
		},
		SSH: func(ctx context.Context, id string) (*mcpserver.SSHConfig, error) {
			f, err := a.prepareSSH(cl, id)
			if err != nil {
				return nil, err
			}
			pc := a.proxyCommand()
			conf := fmt.Sprintf("# anona-compute sandbox %s; certificate valid until %s\nHost %s\n  User %s\n  ProxyCommand %s\n  IdentityFile %s\n  CertificateFile %s\n  IdentitiesOnly yes\n  UserKnownHostsFile %s\n  StrictHostKeyChecking accept-new\n",
				id, f.expiresAt, id, f.user, pc, f.key, f.cert, f.knownHosts)
			var parts []string
			for _, o := range f.options(pc) {
				parts = append(parts, shellQuote(o))
			}
			command := "ssh " + strings.Join(parts, " ") + " " + shellQuote(f.user+"@"+id)
			return &mcpserver.SSHConfig{Host: id, User: f.user, Config: conf, Command: command, ExpiresAt: f.expiresAt,
				IdentityFile: f.key, CertificateFle: f.cert}, nil
		},
	}
	if a.tokens != nil {
		// Browser login: the role decides which tools exist.
		ctx, cancel := context.WithTimeout(a.ctx, 30*time.Second)
		ex, err := a.tokens.Current(ctx)
		cancel()
		if err != nil {
			return cfg, &exitError{code: ExitAuth, err: err}
		}
		if ex.Role == "viewer" {
			cfg.ReadOnly = true
		}
		cfg.Whoami = func(ctx context.Context) (map[string]any, error) {
			ex, err := a.tokens.Current(ctx)
			if err != nil {
				return nil, err
			}
			return map[string]any{"auth": "session", "email": ex.User.Email, "org": ex.Org.Slug, "org_name": ex.Org.Name,
				"role": ex.Role, "api_url": s.APIURL}, nil
		}
	} else {
		cfg.Whoami = func(ctx context.Context) (map[string]any, error) {
			return map[string]any{"auth": "api_key", "api_key": maskKey(s.APIKey), "api_url": s.APIURL}, nil
		}
	}
	return cfg, nil
}

// checkLoopbackAddr validates --http before anything else runs: HOST:PORT
// with a loopback host and a numeric port 1-65535, or 0 for any free one (a
// port name such as "notaport" would otherwise reach the resolver and fail
// with its "unknown port" error).
func checkLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return usageErr("--http must be HOST:PORT, such as 127.0.0.1:8765, not %q", addr)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 0 || n > 65535 {
		return usageErr("--http port must be a number from 1 to 65535 (or 0 for any free port), not %q", port)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return usageErr("--http must be a loopback address such as 127.0.0.1:8765: the server acts with your credentials")
	}
	return nil
}

func (a *app) serveMCPHTTP(srv *mcp.Server, addr string) error {
	if err := checkLoopbackAddr(addr); err != nil {
		return err
	}
	token := a.getenv("ANONA_COMPUTE_MCP_TOKEN")
	generated := token == ""
	if generated {
		var b [24]byte
		if _, err := rand.Read(b[:]); err != nil {
			return err
		}
		token = hex.EncodeToString(b[:])
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, nil)
	want := []byte("Bearer " + token)
	mux := http.NewServeMux()
	mux.Handle("/mcp", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="anona-mcp"`)
			http.Error(w, "missing or wrong bearer token", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	}))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-a.ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	fmt.Fprintf(a.stderr, "anona-compute: MCP server on http://%s/mcp\n", ln.Addr())
	if generated {
		fmt.Fprintf(a.stderr, "anona-compute: send the header  Authorization: Bearer %s\n", token)
	}
	if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
