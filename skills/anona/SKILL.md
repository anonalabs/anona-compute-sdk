---
name: anona
description: Working inside an Anona Compute sandbox. Use when you need to know what this machine is, which tools are installed, how to reach the screen through the computer-use MCP server, or how credentials and network access work here.
---

# Anona Compute sandbox

You are running inside an Anona Compute sandbox: a microVM with its own
kernel, created from the `coding-agent` template. It is
disposable; anything worth keeping must be committed and pushed, or written
where the owner asked.

## Machine

- Ubuntu 24.04, user `user` (uid 1000) with passwordless `sudo`, home `/home/user`.
- Installed: git, gh, Node.js 24 (npm, pnpm), Bun, Python 3.12 with uv and a
  virtualenv at `/opt/venv`, Rust (rustup), Java 21, Docker CLI with buildx
  and compose, Google Chrome, ripgrep, build-essential.
- No Docker daemon and no desktop run by default. `docker` commands need a
  daemon the owner provides.
- `npm install -g` installs into `~/.npm-global`; `pip install` goes into
  `/opt/venv`; `cargo install` into `~/.cargo`.

## Screen (computer-use MCP server)

The `anona-computer-use` MCP server is configured for every installed coding
agent. Tools: `screenshot`, `mouse_move`, `click`, `type_text`, `key`. They act
on the X display in `$DISPLAY` (default `:0`). This template starts no
display, so the tools return an error until one exists. To get a headless
screen (Xvfb, xdotool and scrot are installed):

```bash
Xvfb :0 -screen 0 1280x800x24 >/dev/null 2>&1 &
DISPLAY=:0 google-chrome --no-first-run https://example.com &
```

The current server is a placeholder built on `scrot` and `xdotool`.

## Credentials and network

- Model-provider keys are not stored in this sandbox's environment. Requests
  to providers bound by the owner are authenticated by the platform's egress
  proxy; do not ask for, print or write API keys.
- Outbound network follows the owner's egress policy and may be restricted.
  A failed connection to a host outside the policy is expected, not a bug to
  work around.
- TLS goes through the platform's egress proxy; its CA is in the system
  bundle (`/etc/ssl/certs/ca-certificates.crt`), and `SSL_CERT_FILE`,
  `REQUESTS_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS` and `GIT_SSL_CAINFO` point at it.
