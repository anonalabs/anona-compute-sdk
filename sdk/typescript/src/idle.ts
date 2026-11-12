/**
 * Idle pause: pause a sandbox once nothing has used it for a while.
 *
 * Gateway contract (Anona routes, not E2B):
 * `PUT /v1/sandboxes/{id}/auto-pause` with `{idleMinutes}` returns the sandbox's
 * access settings, and `GET /v1/sandboxes/{id}/access` reports `idlePauseMinutes`.
 * `idleMinutes` is 1 to 10080 (one week); `0` turns idle pausing off.
 *
 * This is **not** E2B's `autoPause`/`onTimeout`, which decides what happens when
 * the sandbox's *deadline* runs out. Idle pause has no deadline: the sandbox runs
 * for as long as it is used and pauses once it is not.
 *
 * What counts as use, restarting the clock: any
 * request to any of the sandbox's ports except envd's `GET /health`; an open live
 * view, terminal, SSH session or byte pipe; and any process envd started that is
 * still running. Not counted: CPU burned inside the guest by something envd did
 * not start, outbound traffic the sandbox makes itself, and API calls *about* the
 * sandbox. A sandbox busy on work it started by itself will still pause.
 *
 * Resume with `Sandbox.connect(sandboxId)`.
 */
import { type ConnectionConfig, InvalidArgumentError } from "e2b";

import { getJson, putJson } from "./http.js";

/** Longest idle time the gateway accepts (store.MaxIdlePauseMinutes): one week. */
export const MAX_IDLE_PAUSE_MINUTES = 10080;

interface AccessResponse {
  idlePauseMinutes?: number;
}

/** `sandbox.idlePause`: read and set this sandbox's idle-pause time. */
export class IdlePause {
  constructor(
    private readonly sandboxId: string,
    private readonly config: ConnectionConfig,
  ) {}

  private get path(): string {
    return `/v1/sandboxes/${encodeURIComponent(this.sandboxId)}`;
  }

  /** Minutes of idleness before this sandbox pauses; `0` when off. */
  async get(): Promise<number> {
    const body = await getJson<AccessResponse>(this.config, `${this.path}/access`);
    return body?.idlePauseMinutes ?? 0;
  }

  /**
   * Pause this sandbox after `minutes` without use. `0` turns it off.
   * Returns the value the gateway recorded, which is what later reads will show.
   */
  async set(minutes: number): Promise<number> {
    if (!Number.isInteger(minutes)) throw new InvalidArgumentError("minutes must be an integer");
    if (minutes < 0 || minutes > MAX_IDLE_PAUSE_MINUTES) {
      throw new InvalidArgumentError(`minutes must be 0 (off) to ${MAX_IDLE_PAUSE_MINUTES}`);
    }
    const body = await putJson<AccessResponse>(this.config, `${this.path}/auto-pause`, {
      idleMinutes: minutes,
    });
    return body?.idlePauseMinutes ?? 0;
  }

  /** Stop pausing this sandbox when it is idle. Same as `set(0)`. */
  async off(): Promise<number> {
    return this.set(0);
  }
}
