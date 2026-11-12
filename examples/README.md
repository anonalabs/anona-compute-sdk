# Examples

Small, runnable programs for [Anona Compute](https://compute.anonalabs.com/docs).
Each one creates a sandbox, does one thing, and deletes the sandbox.

Every example needs an API key from the [console](https://compute.anonalabs.com):

```sh
export E2B_DOMAIN=compute.anonalabs.com
export E2B_API_KEY=ac_...          # the stock e2b SDK reads these
export ANONA_API_KEY="$E2B_API_KEY" # the Anona extras and the REST calls read this
```

| Example | Python | TypeScript |
|---|---|---|
| Create a sandbox and run a command (stock `e2b` SDK) | `python/quickstart.py` | `typescript/quickstart.ts` |
| Pick a size: Small, Medium, Large or XL | `python/sizes.py` | `typescript/sizes.ts` |
| Open a port with a preview link | `python/preview_link.py` | `typescript/preview_link.ts` |
| Watch or drive a sandbox with live view | `python/live_view.py` | `typescript/live_view.ts` |
| Pause a sandbox and resume it | `python/pause_resume.py` | `typescript/pause_resume.ts` |

Python: `pip install anona-compute httpx`, then `python python/quickstart.py`.
TypeScript (from the repository root): `pnpm install`, then `pnpm --filter anona-compute-examples exec tsx quickstart.ts` (or copy a file into your own project with `npm install @anona-labs/compute-sdk e2b`).
