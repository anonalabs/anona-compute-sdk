# @anona-labs/compute-sdk

Optional extras for Anona Compute on top of the stock `e2b` package (peer dependency `e2b ^2.61.0`).
The stock SDK works on its own with `E2B_DOMAIN=compute.anonalabs.com` and `E2B_API_KEY` (an `ac_` API key from https://compute.anonalabs.com).

```sh
npm install @anona-labs/compute-sdk e2b
```

```ts
import { Sandbox, configure, withSize } from "@anona-labs/compute-sdk";

configure({ apiKey: "ac_..." });            // or ANONA_API_KEY / E2B_API_KEY
const sbx = await Sandbox.create("python", withSize("large"));  // subclass of e2b Sandbox

const url = await sbx.live.url({ mode: "dom" });
const tok = await sbx.live.token({ scope: "control", channels: ["pty"], ttl: 120 });
await sbx.live.revokeAll();

const cert = await sbx.ssh.certificate(publicKeyText);
console.log(sbx.ssh.config({ identityFile: "~/.ssh/anona_ed25519" }));
```

Sizes: `small` (1 vCPU, 2 GiB, 10 GB disk), `medium` (2, 4 GiB, 20 GB), `large` (4, 8 GiB, 40 GB), `xl` (8, 16 GiB, 80 GB); without one a sandbox runs at its template's own size.

Environment mapping: `ANONA_API_KEY` → `E2B_API_KEY`, `ANONA_DOMAIN` → `E2B_DOMAIN`
(default `compute.anonalabs.com`), `ANONA_API_URL` → `E2B_API_URL`. `E2B_*` values win.

Development: `pnpm --filter @anona-labs/compute-sdk test` (fake gateway), `pnpm --filter @anona-labs/compute-sdk test:it`
(needs `GATEWAY_URL` and `GATEWAY_API_KEY`). Release: `pnpm --filter @anona-labs/compute-sdk version:tag`
then `pnpm --filter @anona-labs/compute-sdk pack`. Releases are published from https://github.com/anonalabs/anona-compute-sdk by its `sdk-publish` workflow.

This is the library only. The `anona-compute` command-line tool is a separate npm package: `@anona-labs/anona-compute`.
