# Contributing

Thanks for helping. Issues and pull requests are welcome.

- Keep a pull request to one change, with tests: `pnpm --filter @anona-labs/compute-sdk test`,
  `python -m pytest sdk/python/tests -q` and, for the CLI, `cd cli && go test ./...` must pass,
  and all three run in CI.
- Both SDKs extend the stock `e2b` SDK rather than replacing it. Keep that: a change should not
  break code that uses `e2b` directly against Anona Compute.
- Examples must run against the real service as written. If you add one, add it to
  `examples/README.md` in both languages where it makes sense.
- Do not bump versions in a pull request; releases set them.

Some files here are also kept in Anona Labs' internal repository and synchronised both ways
(see `AGENTS.md`), so a maintainer may port your change there after merging.
