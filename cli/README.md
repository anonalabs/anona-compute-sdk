# anona-compute CLI

`anona-compute` creates and uses Anona Compute sandboxes through the E2B-compatible
gateway API, and serves them to AI agents over MCP.

```shell
npm install -g @anona-labs/anona-compute                  # installs the released binary for your platform
```

Other ways to install:

- **Download the binary** for your platform from the
  [releases page](https://github.com/anonalabs/anona-compute-sdk/releases) (tags `cli-v<version>`):
  a `.tar.gz` (Linux, macOS) or `.zip` (Windows) holding the single `anona-compute` binary, and
  `SHA256SUMS` to check it with `sha256sum -c SHA256SUMS --ignore-missing`. Put the binary on
  your `PATH`.
- **Build it with Go** (1.26 or newer):
  `go install github.com/anonalabs/anona-compute-sdk/cli/cmd/anona-compute@latest`. `@latest`
  is the newest release (each release also tags `cli/v<version>`, the tag Go looks for), and
  the binary reports that version.

This directory is mirrored, byte for byte, between Anona's internal repository and the public
[anona-compute-sdk](https://github.com/anonalabs/anona-compute-sdk) repository, and releases are
built only there (`cli-publish` workflow, `scripts/release.sh`).

The binary is `anona-compute` because `anona` is the Anona Memory CLI. The two
are separate programs with separate sign-ins. Nothing is shared: settings live
in `$XDG_CONFIG_HOME/anona-compute/config.toml` (`ANONA_COMPUTE_CONFIG`
overrides it), the browser-login secret in the keychain service `anona-compute`
(or `anona-compute/credentials.json`), SSH keys under `$XDG_RUNTIME_DIR/anona-compute/ssh`,
and the environment variables are `ANONA_COMPUTE_*` (plus the E2B SDK's
`E2B_*`). `ANONA_API_KEY` is read only when it holds an Anona Compute key
(`e2b_...`), so a Memory key in it is never sent to the compute API.

## Sign in

```shell
anona-compute login                 # browser sign-in (device code)
anona-compute whoami                # user, organization, role
anona-compute logout                # revokes the CLI session on the dashboard
```

`anona-compute login` uses the OAuth 2.0 device authorization grant (RFC 8628) served
by the dashboard's Better Auth device-authorization plugin. It prints a URL and
a code (for example `ABCD-EFGH`), opens your browser (`--no-browser` to skip),
and waits. In the browser you sign in, check the code, pick the organization
(when you belong to more than one) and approve. `--org <slug>` picks the
organization from the terminal instead.

What the CLI keeps: a dashboard session for this machine (its user agent names
the host, `CLI on <hostname>`). Every command trades it at
`POST /api/cli/token` for a gateway JWT that lasts 15 minutes, carrying the
organization, your current role and the session (`sid`). Signing out
(`anona-compute logout`), revoking the session in the console (CLI & agent
sessions page), removal from the organization and a role change all reach
the gateway within seconds, also for JWTs already issued: the dashboard
records them in Postgres and the gateway follows that list. After a role
change the CLI (and a running MCP server) fetches a new JWT with the new role
on its own. The session secret is stored in the OS keychain (macOS Keychain,
Windows Credential Manager, the Secret Service on Linux) when one works, else
in `credentials.json` (mode 0600) next to `config.toml`.
`ANONA_COMPUTE_CREDENTIAL_STORE=file|keyring` forces one. `config.toml` only records
that the profile is signed in, the organization and the email.

API keys keep working and win over a browser login when set:

```shell
anona-compute login --with-api-key < key.txt     # saves the key in the profile and verifies it
ANONA_COMPUTE_API_KEY=e2b_... anona-compute list          # or E2B_API_KEY, or --api-key
```

The dashboard is `--web-url`, else `ANONA_COMPUTE_WEB_URL`, else the profile's
`web_url`, else `https://app.<domain>`. The API is `--api-url`, else
`ANONA_COMPUTE_API_URL` / `E2B_API_URL`, else the profile, else `https://api.<domain>`.

The console page `/<org>/sessions` lists every machine signed in this way
(host name, organization, current role, signed in, last used) and revokes one
or all of them; owners and admins also see and revoke members' sessions in
the organization.

## Sandboxes

```shell
anona-compute create -t python --timeout 1h          # or: anona-compute sandbox create ...
anona-compute list                                   # or: anona-compute sandbox list
anona-compute exec <id> -- python -V
tar -cz src | anona-compute exec -i <id> -- 'tar -xz'  # -i sends stdin to the command
anona-compute exec -b <id> -- 'npm run dev'          # background: prints the PID
anona-compute output <id> <pid> -f                   # its output (ps <id> lists processes)
anona-compute run -t base -- uname -a                # create, run, delete
anona-compute connect <id>                           # interactive shell over envd
anona-compute ssh <id>                               # OpenSSH, 10-minute certificate; scp works the same way
anona-compute view <id> --mode terminal --control    # live-view URL; desktop <id> for the desktop template
anona-compute logs <id> -f
anona-compute delete <id>                            # or: kill, rm
```

`anona-compute --help` lists every command; `--json` gives one JSON object per line,
with snake_case keys (`sandbox_id`, `exit_code`). `anona-compute doctor` checks the
configuration, the credential and the connection. `exec` and `run` exit with the
remote command's exit code, and 125 when the CLI itself fails; other commands exit
2 (usage), 3 (auth), 4 (not found), 5 (quota) or 1. `keys`, `limits`, `push` and
`recover` are listed but not built yet (they exit 1 and say so).

## Usage and billing

```shell
anona-compute usage                      # this month: meters, total, not yet rated, projection, top sandboxes
anona-compute usage --period 2026-09     # a closed month
anona-compute usage --json               # the whole document (daily, per sandbox, per template)
anona-compute usage --billing            # plan, rates, invoices
```

The gateway serves `GET /v1/usage` and `GET /v1/billing` from the metering
service. Usage is open to every role of a browser login and to API keys with
the `usage:read` scope (the "Read only" and "Full access" presets have it);
billing to owners and admins and to keys with `billing:read` ("Full access").
Keys created before these scopes existed do not have them.

## Templates

```shell
anona-compute template build ./my-env -f ./my-env/Dockerfile --name my-env   # uploads the context, streams the build log
anona-compute create -t my-env
anona-compute template list
anona-compute template info my-env           # builds and their status
anona-compute template logs my-env           # newest build's log (follows a running build)
anona-compute template delete my-env --yes
```

The gateway builds the Dockerfile with BuildKit in an isolated builder, pins
every `FROM` by digest, adds the Anona runtime (envd) and registers the image
as a sandbox template. An unchanged context is
not uploaded twice. Templates built with the E2B SDK's `Template.build` show
up here too.

## MCP server for AI agents

`anona-compute mcp` is a Model Context Protocol server (official Go SDK,
`github.com/modelcontextprotocol/go-sdk`) that acts with the CLI's credential.
Sign in once with `anona-compute login` (or set an API key), then register it with your
agent.

Claude Code:

```shell
claude mcp add anona-compute -- anona-compute mcp
```

Codex (`codex mcp add`, or `~/.codex/config.toml`):

```shell
codex mcp add anona-compute -- anona-compute mcp
```

```toml
[mcp_servers.anona-compute]
command = "anona-compute"
args = ["mcp"]
```

Cursor (`~/.cursor/mcp.json`, or `.cursor/mcp.json` in a project):

```json
{
  "mcpServers": {
    "anona-compute": { "command": "anona-compute", "args": ["mcp"] }
  }
}
```

Any other client that starts stdio servers: command `anona-compute`, arguments `mcp`.
Add `--profile <name>` before `mcp` to use another profile, or `--read-only`
after it to offer only the read-only tools.

Streamable HTTP, for clients that connect to a URL:

```shell
ANONA_COMPUTE_MCP_TOKEN=$(openssl rand -hex 24) anona-compute mcp --http 127.0.0.1:8765
# endpoint http://127.0.0.1:8765/mcp, header Authorization: Bearer $ANONA_COMPUTE_MCP_TOKEN
```

Only loopback addresses are accepted, and every request needs the bearer token
(a random one is printed when `ANONA_COMPUTE_MCP_TOKEN` is not set), because the server
acts with your credentials.

Tools:

| Tool | What it does |
|---|---|
| `whoami` | user, organization and role (or the masked API key) |
| `list_sandboxes`, `get_sandbox` | state, template, size, end time (absent = no limit), metadata |
| `list_templates` | the organization's own templates: names, build status, CPU and memory |
| `create_sandbox` | template, `timeout_seconds` (`-1` = no limit), `internet` on/off, metadata, env vars, auto-pause, `idle_pause_minutes` |
| `kill_sandbox`, `pause_sandbox`, `resume_sandbox`, `extend_sandbox` | lifecycle; extend takes `-1` for no limit |
| `run_command` | runs `bash -l -c`, returns stdout, stderr, exit code; `timeout_seconds` (default 60) |
| `start_process`, `read_process_output`, `kill_process` | background processes; output read by offset, optional wait |
| `read_file`, `write_file`, `list_files` | text or base64 content, directory listings |
| `get_live_view_url` | short-lived view (or control) link for desktop, dom or terminal |
| `get_sandbox_logs` | the sandbox's process and envd log, newest last; `level` and `search` filters, `limit` |
| `get_sandbox_metrics` | latest CPU, memory and disk sample from inside the sandbox, plus kept samples |
| `get_preview_url` | shareable link to one port of a running sandbox, until `ttl_seconds` (default 3600, max 86400) |
| `get_usage` | usage and cost for a billing period (meters, totals, projection, top sandboxes) |
| `get_ssh_config` | signs a 10-minute SSH certificate and returns an ssh_config block |
| `desktop_screenshot` | PNG of the desktop template's X display (image content) |

Internet access is set at creation only (`create_sandbox`'s `internet`, or `--no-internet` on `anona-compute create`/`run`). `get_sandbox` and `list_sandboxes` do not return it.

Every call goes to the gateway with your credential, so the gateway's tenant,
role and API key scope checks decide what works: another organization's
sandbox is "not found", and a key without `sandboxes:use` cannot run commands.
A viewer gets only `whoami`, `list_sandboxes`, `get_sandbox`, `list_templates`, `get_usage`
and view-only `get_live_view_url`. Background process output is kept by the MCP server
process (last 4 MiB per stream), so it is lost when the agent restarts it.

## Tests

```shell
go test ./...                                  # unit tests (fake gateway and dashboard), no network
scripts/release.sh 0.0.0                       # cross-compile all six release targets into dist/
```

The end-to-end tests in `internal/mcpserver/*_e2e_test.go` skip themselves unless
`ANONA_MCP_E2E_BIN` names a built binary and the environment points the CLI at a running gateway;
Anona runs them against its own gateway, a real envd and a real browser login.
