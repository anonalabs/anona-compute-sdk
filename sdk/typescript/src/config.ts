import { type CapacityWaitOpts, setCapacityWaitDefaults } from "./capacity.js";

/**
 * Environment mapping between Anona settings and the variables the E2B SDK reads.
 *
 * The e2b 2.61.0 JS SDK reads `E2B_API_KEY`, `E2B_DOMAIN`, `E2B_API_URL`
 * (deprecated), `E2B_SANDBOX_URL`, `E2B_DEBUG` and `E2B_HTTP_VERSION` through
 * `getEnvVar` in `ConnectionConfig`. Only the first three are mapped; no new
 * E2B variables are invented. `E2B_*` values always win over `ANONA_*`.
 */

/** Domain customers set as `E2B_DOMAIN`; the SDK derives `https://api.<domain>`. */
export const DEFAULT_DOMAIN = "compute.anonalabs.com";

/** Public API endpoint. */
export const DEFAULT_API_URL = `https://api.${DEFAULT_DOMAIN}`;

/** Environment variable the stock E2B SDK reads for the domain. */
export const DOMAIN_ENV = "E2B_DOMAIN";

/** Anona variable -> E2B variable. */
export const ENV_MAP = {
  ANONA_API_KEY: "E2B_API_KEY",
  ANONA_DOMAIN: "E2B_DOMAIN",
  ANONA_API_URL: "E2B_API_URL",
} as const;

export interface AnonaDefaults {
  apiKey?: string;
  domain?: string;
  apiUrl?: string;
}

type Env = Record<string, string | undefined>;

function env(): Env {
  return typeof process === "undefined" ? {} : process.env;
}

/**
 * `value` if it is an Anona Compute key (`ac_`, legacy `e2b_`), else
 * undefined. `ANONA_API_KEY` is shared with Anona Memory, whose key must never
 * be sent to the compute API; the `anona-compute` CLI applies the same rule.
 */
export function computeKey(value: string | undefined): string | undefined {
  return value && /^(ac_|e2b_)/.test(value) ? value : undefined;
}

/** Connection options that point the SDK at Anona unless `E2B_*` says otherwise. */
export function anonaDefaults(e: Env = env()): AnonaDefaults {
  const out: AnonaDefaults = {};
  if (!e.E2B_DOMAIN) out.domain = e.ANONA_DOMAIN || DEFAULT_DOMAIN;
  const key = computeKey(e.ANONA_API_KEY);
  if (!e.E2B_API_KEY && key) out.apiKey = key;
  if (!e.E2B_API_URL && e.ANONA_API_URL) out.apiUrl = e.ANONA_API_URL;
  return out;
}

/**
 * Copy `ANONA_*` values to the `E2B_*` names and default `E2B_DOMAIN`.
 * `ANONA_API_KEY` is copied only when it holds a compute key.
 */
export function applyEnv(e: Env = env()): void {
  for (const [src, dst] of Object.entries(ENV_MAP)) {
    const value = src === "ANONA_API_KEY" ? computeKey(e[src]) : e[src];
    if (value && !e[dst]) e[dst] = value;
  }
  if (!e.E2B_DOMAIN) e.E2B_DOMAIN = DEFAULT_DOMAIN;
}

export interface ConfigureOpts extends CapacityWaitOpts {
  apiKey?: string;
  domain?: string;
  apiUrl?: string;
  debug?: boolean;
}

/**
 * Point the stock E2B SDK (and the Anona extras) at Anona Compute by setting
 * the `E2B_*` variables of this process.
 *
 * `waitForCapacityMs` (`0` turns it off) and `onCapacityWait` set the default
 * capacity wait of `Sandbox.create` and `Sandbox.connect` in this process; an
 * option passed to the call wins.
 */
export function configure(opts: ConfigureOpts = {}): void {
  setCapacityWaitDefaults(opts);
  const e = env();
  if (opts.apiKey !== undefined) e.E2B_API_KEY = opts.apiKey;
  if (opts.domain !== undefined) e.E2B_DOMAIN = opts.domain;
  if (opts.apiUrl !== undefined) e.E2B_API_URL = opts.apiUrl;
  if (opts.debug !== undefined) e.E2B_DEBUG = opts.debug ? "true" : "false";
  applyEnv(e);
}
