import { type ConnectionOpts, Sandbox as E2BSandbox } from "e2b";

import { anonaDefaults } from "./config.js";
import { IdlePause } from "./idle.js";
import { Live } from "./live.js";
import { Ssh } from "./ssh.js";

/** The stock e2b `Sandbox` with Anona defaults, plus `live`, `ssh` and `idlePause`. */
export class Sandbox extends E2BSandbox {
  protected static override resolveOpts<T extends ConnectionOpts>(opts?: T): T | undefined {
    const resolved = super.resolveOpts(opts) ?? ({} as T);
    const merged: Record<string, unknown> = { ...anonaDefaults() };
    for (const [k, v] of Object.entries(resolved)) if (v !== undefined) merged[k] = v;
    return merged as T;
  }

  /** Live view: `token()`, `url()`, `revokeAll()`. */
  get live(): Live {
    return new Live(this.sandboxId, this.connectionConfig);
  }

  /** SSH: `certificate(publicKey)` and `config()`. */
  get ssh(): Ssh {
    return new Ssh(this.sandboxId, this.connectionConfig);
  }

  /**
   * Idle pause: `get()`, `set(minutes)`, `off()`.
   *
   * Pauses the sandbox once nothing has used it for that long. Different from
   * E2B's `onTimeout`, which acts when the deadline runs out.
   */
  get idlePause(): IdlePause {
    return new IdlePause(this.sandboxId, this.connectionConfig);
  }

  /** Resolved connection options (for diagnostics and tests). */
  static connectionDefaults(opts?: ConnectionOpts): ConnectionOpts | undefined {
    return this.resolveOpts(opts);
  }
}
