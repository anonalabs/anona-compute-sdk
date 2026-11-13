# Working in this repository

This is the public half of Anona Compute's client code. The same files also live in Anona Labs'
private monorepo, and **the two copies must stay in sync**.

| This repository | Private monorepo |
|---|---|
| `sdk/python/` | `sdk/python/` |
| `sdk/typescript/` | `sdk/typescript/` |
| `sdk/package-check.sh` | `sdk/package-check.sh` |
| `skills/anona/` | the agent images' skill folder |
| `examples/` | `examples/` |
| `cli/` | `cli/` |

**Rule: a change to any of these paths, in either repository, is applied to the other one in the
same piece of work.** After merging a pull request here, import it into the private monorepo
(`scripts/public-sdk-sync.sh import` there) and open a PR for it; after merging a change there, export it
here (`scripts/public-sdk-sync.sh export`). Nothing outside these paths is ever copied
between the repositories, and nothing internal (hostnames, IPs, account or instance IDs, bucket
names, internal ticket numbers) may land here: the sync script scans for that and refuses.

Other rules:

- Releases come only from this repository: `sdk-publish` for the SDKs (one version covers both
  packages and the `sdk-v<version>` tag) and `cli-publish` for the CLI (GitHub release
  `cli-v<version>`, Go module tag `cli/v<version>`, then the npm launcher and its six platform
  packages). Dry run first, then a real run from `main`.
- `cli/` is one Go module, `github.com/anonalabs/anona-compute-sdk/cli`, under the same path in
  both repositories, so the copies are byte-identical. Its npm packages are in `cli/npm/`: the
  launcher `@anona-labs/anona-compute` only runs the binary from whichever platform package npm
  installed as an optional dependency, and downloads nothing. Keep `cli/` self-contained: no
  references to code that is not in this repository.
- Both SDKs are thin extras over the stock `e2b` SDK; keep stock `e2b` usage working.
- Examples must run against the real service as written.
