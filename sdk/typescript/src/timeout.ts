/**
 * `timeoutMs: -1` means no deadline.
 *
 * The gateway's "no deadline" is a timeout of `-1` seconds (`store.NoTimeout`),
 * accepted on create, connect, fork and `POST /sandboxes/{id}/timeout` while the
 * team has no maximum lifetime. e2b sends `Math.ceil(timeoutMs / 1000)`, which
 * turns `-1` ms into `-0`, serialised as `0`: a create the gateway refuses with
 * a 400 and a `setTimeout` that ends the sandbox at once. So `-1` is passed on
 * as `-1000` ms, which e2b converts to the `-1` the gateway expects.
 *
 * `-1000` itself also means no deadline: it is the gateway's `-1` seconds in
 * milliseconds, and the value that worked on SDK versions before this one.
 * Any other negative value is refused before a request is sent: e2b would turn
 * most of them into `0`, which ends the sandbox now.
 */
import { InvalidArgumentError } from "e2b";

/** The `timeoutMs` that means no deadline. */
export const NO_DEADLINE_MS = -1;
/** What e2b must be given for the gateway to receive `-1` seconds. */
const WIRE_NO_DEADLINE_MS = -1000;

/** `timeoutMs` as e2b must receive it so the gateway gets the intended timeout. */
export function wireTimeoutMs(timeoutMs: number): number;
export function wireTimeoutMs(timeoutMs: number | undefined): number | undefined;
export function wireTimeoutMs(timeoutMs: number | undefined): number | undefined {
  if (timeoutMs === NO_DEADLINE_MS || timeoutMs === WIRE_NO_DEADLINE_MS) return WIRE_NO_DEADLINE_MS;
  if (timeoutMs !== undefined && timeoutMs < 0) {
    throw new InvalidArgumentError(`timeoutMs must be >= 0, or -1 for no deadline (got ${timeoutMs})`);
  }
  return timeoutMs;
}

/** `opts` with its `timeoutMs` made ready for e2b, as {@link wireTimeoutMs}. */
export function withWireTimeout<T extends { timeoutMs?: number }>(opts: T | undefined): T | undefined {
  if (opts?.timeoutMs === undefined) return opts;
  return { ...opts, timeoutMs: wireTimeoutMs(opts.timeoutMs) };
}
