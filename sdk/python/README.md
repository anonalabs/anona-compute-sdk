# anona-compute (Python)

Optional extras for [Anona Compute](https://compute.anonalabs.com) on top of the stock E2B SDK.
The stock SDK already works on its own:

```sh
export E2B_DOMAIN=compute.anonalabs.com
export E2B_API_KEY=ac_...          # an API key from https://compute.anonalabs.com
```

This package adds sandbox sizes, live view tokens and SSH certificates.

```sh
pip install anona-compute
```

```python
import anona_compute

anona_compute.configure(api_key="ac_...")       # or set ANONA_API_KEY / E2B_API_KEY
sbx = anona_compute.Sandbox.create("python", size="large")  # a subclass of e2b.Sandbox

url = sbx.live.url(mode="dom")          # view-only embed URL, 600 s
tok = sbx.live.token(scope="control", channels=["pty"], ttl=120)
sbx.live.revoke_all()

cert = sbx.ssh.certificate("~/.ssh/anona_ed25519.pub")
cert.write("~/.ssh/anona_ed25519-cert.pub")
print(sbx.ssh.config(identity_file="~/.ssh/anona_ed25519"))
```

Environment mapping: `ANONA_API_KEY` → `E2B_API_KEY`, `ANONA_DOMAIN` → `E2B_DOMAIN`
(default `compute.anonalabs.com`), `ANONA_API_URL` → `E2B_API_URL` (deprecated by E2B, fallback only).
`E2B_*` values always win.

`size` is `small` (1 vCPU, 2 GiB, 10 GB disk), `medium` (2, 4 GiB, 20 GB), `large` (4, 8 GiB, 40 GB) or `xl` (8, 16 GiB, 80 GB). Without it a sandbox runs at its template's own size.

When every host is full the API answers `503` with `Should-Retry: true` while it starts another.
`Sandbox.create` and `Sandbox.connect` then wait and retry after each `Retry-After` (held between
1 and 30 s) for up to 10 minutes: `wait_for_capacity=` (seconds) and `on_capacity_wait=` per call
or in `configure()`, or `ANONA_COMPUTE_CAPACITY_WAIT`; `0` turns it off. Each wait is logged at
`INFO` on the `anona_compute` logger. Other errors, plan limits included, are raised at once.

Install the desktop extra with `pip install "anona-compute[desktop]"`.

This is the Python library only. The `anona-compute` command-line tool is a separate npm package: `npm install -g @anona-labs/anona-compute`.

## Development

```sh
uv venv -p 3.12 .venv && uv pip install -p .venv/bin/python -e ".[test]"
.venv/bin/python -m pytest -q          # fake gateway, no network
GATEWAY_URL=... GATEWAY_API_KEY=... .venv/bin/python -m pytest -m gateway
```

The version comes from the git tag `sdk-v<version>` (hatch-vcs); untagged builds get a `0.0.1.devN+g<sha>` development version.
