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
| `cli` | `anona-compute`, the command-line tool and MCP server for agents (Go) | `npm install -g @anona-labs/anona-compute`, a binary from [Releases](https://github.com/anonalabs/anona-compute-sdk/releases), or `go install github.com/anonalabs/anona-compute-sdk/cli/cmd/anona-compute@latest` |

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

**The command-line tool and the Python library share a name but are different packages.** The
CLI (`cli/`) is the npm package `@anona-labs/anona-compute`, or a download from
[Releases](https://github.com/anonalabs/anona-compute-sdk/releases); the PyPI package
`anona-compute` is the Python library only and installs no command. See [cli/README.md](cli/README.md).

## Development

```sh
pnpm install                                         # TypeScript workspace (sdk + examples)
pnpm --filter @anona-labs/compute-sdk lint
pnpm --filter @anona-labs/compute-sdk test           # against a local fake gateway, no network

uv venv -p 3.12 .venv && uv pip install -p .venv/bin/python -e "sdk/python[test]"
.venv/bin/python -m pytest sdk/python/tests -q       # same, for Python

cd cli && go test ./...                              # the CLI (Go 1.26), no network
cli/scripts/release.sh 0.0.0                         # cross-compile all six CLI targets into cli/dist/
```

Releases are published by two workflows, each a dry run first and then a real run from `main`:

- `sdk-publish`: both SDK packages at one version, tagged `sdk-v<version>`, with npm provenance
  and PyPI attestations.
- `cli-publish`: the CLI. A GitHub release `cli-v<version>` with the six platform archives and
  `SHA256SUMS`, a `cli/v<version>` tag for `go install`, then `@anona-labs/anona-compute` and its
  six platform packages on npm with provenance.

## License

[Apache License 2.0](LICENSE).
