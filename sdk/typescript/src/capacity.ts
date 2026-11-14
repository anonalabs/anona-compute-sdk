/**
 * Wait for capacity: retry a sandbox create or connect while the API says to.
 *
 * When every host is full the API answers a create with `503`,
 * `Should-Retry: true` and `Retry-After` while it starts another host, which
 * takes a few minutes. The e2b SDK retries such an answer a few times within
 * one request timeout (a minute by default) and then throws. This keeps
 * retrying, after each `Retry-After`, until a deadline: 10 minutes unless the
 * caller says otherwise, `0` to turn it off.
 *
 * Only a `503` marked `Should-Retry: true` is retried. Every other error,
 * including a `503` with `Should-Retry: false` or without the header (an
 * answer from something in front of the API, after which a sandbox may
 * already have started) and every plan limit (`403` / `429`
 * `plan_limit_exceeded`), is thrown at once.
 *
 * The e2b SDK's errors carry no response headers, so the headers are read
 * from Node's `undici:request:headers` diagnostics channel, which every fetch
 * the SDK makes on Node publishes, for the requests made inside one attempt.
 * Where that channel is missing (a browser), the wait is off and errors are
 * thrown as before.
 */
import type { Logger } from "e2b";

/** Milliseconds a create or connect keeps retrying by default. */
export const DEFAULT_WAIT_FOR_CAPACITY_MS = 600_000;

/**
 * Environment variable with the default wait, the same one the CLI reads:
 * seconds (`600`) or a duration (`10m`, `90s`); `0` turns the wait off.
 */
export const CAPACITY_WAIT_ENV = "ANONA_COMPUTE_CAPACITY_WAIT";

const MIN_DELAY_MS = 1_000;
const MAX_DELAY_MS = 30_000;
const DEFAULT_DELAY_MS = 5_000;

/** What `onCapacityWait` is told before each wait. */
export interface CapacityWaitInfo {
  /** 1 for the first retry, 2 for the second, and so on. */
  attempt: number;
  /** Milliseconds until the next try. */
  delayMs: number;
  /** Milliseconds since the first try. */
  elapsedMs: number;
  /** Milliseconds left before the wait gives up. */
  remainingMs: number;
  /** HTTP status of the refusal (503). */
  status: number;
  /** The error the try failed with. */
  error: unknown;
}

export type OnCapacityWait = (info: CapacityWaitInfo) => void;

/** Options of `Sandbox.create` and `Sandbox.connect` for the capacity wait. */
export interface CapacityWaitOpts {
  /**
   * How long, in milliseconds, a create or connect keeps retrying while the
   * API answers `503` with `Should-Retry: true`. `0` turns the wait off.
   * Default: `configure({ waitForCapacityMs })`, else `ANONA_COMPUTE_CAPACITY_WAIT`,
   * else 10 minutes.
   */
  waitForCapacityMs?: number;
  /** Called before each wait. */
  onCapacityWait?: OnCapacityWait;
}

const defaults: CapacityWaitOpts = {};

/** Process-wide defaults, set by `configure()`. */
export function setCapacityWaitDefaults(opts: CapacityWaitOpts): void {
  if (opts.waitForCapacityMs !== undefined) defaults.waitForCapacityMs = checkWait(opts.waitForCapacityMs);
  if (opts.onCapacityWait !== undefined) defaults.onCapacityWait = opts.onCapacityWait;
}

/** Clears what `configure()` set (for tests). */
export function resetCapacityWaitDefaults(): void {
  delete defaults.waitForCapacityMs;
  delete defaults.onCapacityWait;
}

function checkWait(ms: number): number {
  if (!Number.isFinite(ms) || ms < 0) throw new RangeError("waitForCapacityMs must be 0 or more milliseconds");
  return ms;
}

const UNIT_MS: Record<string, number> = { ms: 1, s: 1_000, m: 60_000, h: 3_600_000 };

/** Milliseconds in `value`: a number of seconds or a Go-style duration (`1m30s`). */
export function parseDurationMs(value: string | undefined): number | undefined {
  const v = (value ?? "").trim();
  if (!v) return undefined;
  if (/^\d+(\.\d+)?$/.test(v)) return Number(v) * 1_000;
  const parts = [...v.matchAll(/(\d+(?:\.\d+)?)(ms|s|m|h)/g)];
  if (parts.length === 0 || parts.map((p) => p[0]).join("") !== v) return undefined;
  return parts.reduce((sum, p) => sum + Number(p[1]) * (UNIT_MS[p[2] ?? "s"] ?? 0), 0);
}

/** The wait for one call: the option, else `configure()`, else the environment, else 10 minutes. */
export function resolveWaitMs(perCall?: number): number {
  if (perCall !== undefined) return checkWait(perCall);
  if (defaults.waitForCapacityMs !== undefined) return defaults.waitForCapacityMs;
  const env = typeof process === "undefined" ? undefined : process.env[CAPACITY_WAIT_ENV];
  return parseDurationMs(env) ?? DEFAULT_WAIT_FOR_CAPACITY_MS;
}

/** The last response seen by the requests of one attempt. */
interface Capture {
  status?: number;
  shouldRetry?: string;
  retryAfter?: string;
}

interface Store<T> {
  run<R>(store: T, fn: () => R): R;
  getStore(): T | undefined;
}

let store: Store<Capture> | undefined;
let installed = false;

type BuiltinLoader = (id: string) => unknown;

/** Subscribe to undici's diagnostics channels once. Returns whether headers can be read. */
function install(): boolean {
  if (installed) return store !== undefined;
  installed = true;
  try {
    const load = (globalThis as { process?: { getBuiltinModule?: BuiltinLoader } }).process?.getBuiltinModule;
    if (!load) return false;
    const dc = load("node:diagnostics_channel") as {
      subscribe(name: string, fn: (message: unknown) => void): void;
    };
    const hooks = load("node:async_hooks") as { AsyncLocalStorage: new <T>() => Store<T> };
    if (!dc?.subscribe || !hooks?.AsyncLocalStorage) return false;
    const als = new hooks.AsyncLocalStorage<Capture>();
    const owners = new WeakMap<object, Capture>();
    dc.subscribe("undici:request:create", (m) => {
      const capture = als.getStore();
      const request = (m as { request?: object }).request;
      if (!capture || !request) return;
      owners.set(request, capture);
      // A new request: what an earlier one said no longer describes the error.
      delete capture.status;
      delete capture.shouldRetry;
      delete capture.retryAfter;
    });
    dc.subscribe("undici:request:headers", (m) => {
      const { request, response } = m as { request?: object; response?: { statusCode?: number; headers?: unknown[] } };
      const capture = request ? owners.get(request) : undefined;
      if (!capture || !response) return;
      capture.status = response.statusCode;
      const h = response.headers ?? [];
      for (let i = 0; i + 1 < h.length; i += 2) {
        const name = String(h[i]).toLowerCase();
        if (name === "should-retry") capture.shouldRetry = String(h[i + 1]);
        else if (name === "retry-after") capture.retryAfter = String(h[i + 1]);
      }
    });
    store = als;
  } catch {
    store = undefined;
  }
  return store !== undefined;
}

function retryable(c: Capture): boolean {
  return c.status === 503 && (c.shouldRetry ?? "").trim().toLowerCase() === "true";
}

/** `Retry-After` held between 1 and 30 seconds, with up to 10% jitter. */
export function retryDelayMs(retryAfter: string | undefined, random: () => number = Math.random): number {
  const s = Number((retryAfter ?? "").trim());
  let base = retryAfter !== undefined && retryAfter.trim() !== "" && Number.isFinite(s) ? s * 1_000 : DEFAULT_DELAY_MS;
  base = Math.min(Math.max(base, MIN_DELAY_MS), MAX_DELAY_MS);
  return Math.min(Math.max(Math.round(base * (0.9 + 0.2 * random())), MIN_DELAY_MS), MAX_DELAY_MS);
}

/** Seams for tests. `random` is the jitter's source, in [0, 1). */
export const clock = {
  now: (): number => performance.now(),
  random: (): number => Math.random(),
  sleep: (ms: number, signal?: AbortSignal): Promise<void> =>
    new Promise((resolve, reject) => {
      if (signal?.aborted) return reject(signal.reason);
      const onAbort = () => {
        clearTimeout(t);
        reject(signal?.reason);
      };
      const t = setTimeout(() => {
        signal?.removeEventListener("abort", onAbort);
        resolve();
      }, ms);
      signal?.addEventListener("abort", onAbort, { once: true });
    }),
};

/**
 * Run `attempt`, retrying while it fails on a `503` marked `Should-Retry:
 * true`, until the wait runs out; then the last error is thrown unchanged.
 * `signal` (the caller's) cancels a wait in progress.
 */
export async function withCapacityWait<T>(
  what: string,
  opts: (CapacityWaitOpts & { signal?: AbortSignal; logger?: Logger }) | undefined,
  attempt: () => Promise<T>,
): Promise<T> {
  const budget = resolveWaitMs(opts?.waitForCapacityMs);
  const onWait = opts?.onCapacityWait ?? defaults.onCapacityWait;
  if (budget <= 0 || !install() || !store) return attempt();
  const als = store;
  const start = clock.now();
  for (let n = 1; ; n++) {
    const capture: Capture = {};
    try {
      return await als.run(capture, attempt);
    } catch (error) {
      if (!retryable(capture) || opts?.signal?.aborted) throw error;
      const elapsedMs = clock.now() - start;
      const remainingMs = budget - elapsedMs;
      if (remainingMs <= 0) throw error;
      const delayMs = Math.min(retryDelayMs(capture.retryAfter, clock.random), remainingMs);
      opts?.logger?.info?.(
        `${what}: the service is not ready (no capacity free yet, or starting); retrying in ` +
          `${Math.round(delayMs / 1000)}s, ${Math.round(elapsedMs / 1000)}s of ${Math.round(budget / 1000)}s waited`,
      );
      onWait?.({ attempt: n, delayMs, elapsedMs, remainingMs, status: capture.status ?? 503, error });
      await clock.sleep(delayMs, opts?.signal);
    }
  }
}
