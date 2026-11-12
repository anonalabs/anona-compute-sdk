# Anona Compute SDK

Client libraries, an agent skill and examples for [Anona Compute](https://compute.anonalabs.com):
E2B-compatible sandboxes (KVM microVMs) for AI agents and code execution.

Documentation: **https://compute.anonalabs.com/docs**

## What is in this repository

| Path | What it is | Install |
|---|---|---|
| `sdk/python` | `anona-compute`, Python extras on top of the stock `e2b` SDK: sandbox sizes, live view and SSH certificates | `pip install anona-compute` |
| `sdk/typescript` | `@anona-labs/compute-sdk`, the same extras for TypeScript / JavaScript | `npm install @anona-labs/compute-sdk e2b` |
| `skills/anona` | An agent skill (`SKILL.md`) that tells a coding agent what an Anona Compute sandbox is and how to work in it | Copy into your agent's skills folder |
| `examples/` | Small runnable programs: create and run, sizes, preview links, live view, pause and resume | See [examples/README.md](examples/README.md) |

You do not need these libraries to use Anona Compute: the stock `e2b` SDK works with two
environment variables.

```sh
export E2B_DOMAIN=compute.anonalabs.com
export E2B_API_KEY=ac_...          # an API key from https://compute.anonalabs.com
```

```python
from e2b import Sandbox

sbx = Sandbox.create()
print(sbx.commands.run("uname -sr").stdout)
sbx.kill()
```

**The command-line tool is a separate package.** `anona-compute`, the CLI and MCP server, is
installed from npm with `npm install -g @anona-labs/anona-compute`; it is not built from this
repository. The PyPI package `anona-compute` in this repository is the Python library only and
installs no command.

## Development

```sh
pnpm install                                         # TypeScript workspace (sdk + examples)
pnpm --filter @anona-labs/compute-sdk lint
pnpm --filter @anona-labs/compute-sdk test           # against a local fake gateway, no network

uv venv -p 3.12 .venv && uv pip install -p .venv/bin/python -e "sdk/python[test]"
.venv/bin/python -m pytest sdk/python/tests -q       # same, for Python
```

Releases are published by the `sdk-publish` workflow (one version for both packages, tagged
`sdk-v<version>`), with npm provenance and PyPI attestations.

## License

[Apache License 2.0](LICENSE).
