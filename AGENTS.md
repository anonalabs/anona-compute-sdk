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

**Rule: a change to any of these paths, in either repository, is applied to the other one in the
same piece of work.** After merging a pull request here, import it into the private monorepo
(`scripts/public-sdk-sync.sh import` there) and open a PR for it; after merging a change there, export it
here (`scripts/public-sdk-sync.sh export`). Nothing outside these paths is ever copied
between the repositories, and nothing internal (hostnames, IPs, account or instance IDs, bucket
names, internal ticket numbers) may land here: the sync script scans for that and refuses.

Other rules:

- Releases come only from this repository's `sdk-publish` workflow (dry run first, then a real
  run from `main`). One version covers both packages and the `sdk-v<version>` tag.
- Both SDKs are thin extras over the stock `e2b` SDK; keep stock `e2b` usage working.
- Examples must run against the real service as written.
