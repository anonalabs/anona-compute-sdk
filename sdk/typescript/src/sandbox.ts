import { type ConnectionOpts, Sandbox as E2BSandbox, type SandboxConnectOpts, type SandboxOpts } from "e2b";

import { type CapacityWaitOpts, withCapacityWait } from "./capacity.js";
import { anonaDefaults } from "./config.js";
import { IdlePause } from "./idle.js";
import { Live } from "./live.js";
import { Ssh } from "./ssh.js";

/** `Sandbox.create` options: e2b's, plus the capacity wait. */
export type AnonaSandboxOpts = SandboxOpts & CapacityWaitOpts;
/** `Sandbox.connect` options: e2b's, plus the capacity wait. */
export type AnonaSandboxConnectOpts = SandboxConnectOpts & CapacityWaitOpts;

type CreateResponse = Awaited<ReturnType<(typeof E2BSandbox)["createSandbox"]>>;

/**
 * The stock e2b `Sandbox` with Anona defaults, plus `live`, `ssh` and
 * `idlePause`. `create` and `connect` wait for capacity: while the fleet is
 * full they retry a `503` marked `Should-Retry: true` after its `Retry-After`,
 * for up to `waitForCapacityMs` (10 minutes by default, `0` turns it off).
 */
export class Sandbox extends E2BSandbox {
  protected static override resolveOpts<T extends ConnectionOpts>(opts?: T): T | undefined {
    const resolved = super.resolveOpts(opts) ?? ({} as T);
    const merged: Record<string, unknown> = { ...anonaDefaults() };
    for (const [k, v] of Object.entries(resolved)) if (v !== undefined) merged[k] = v;
    return merged as T;
  }

  /**
   * Create a sandbox (e2b's `Sandbox.create`). While the fleet is full the
   * create waits and tries again, for up to `waitForCapacityMs`, calling
   * `onCapacityWait` before each wait; `signal` cancels the wait.
   */
  static override create<S extends typeof E2BSandbox>(this: S, opts?: AnonaSandboxOpts): Promise<InstanceType<S>>;
  static override create<S extends typeof E2BSandbox>(
    this: S,
    template: string,
    opts?: AnonaSandboxOpts,
  ): Promise<InstanceType<S>>;
  static override create(templateOrOpts?: string | AnonaSandboxOpts, opts?: AnonaSandboxOpts) {
    return typeof templateOrOpts === "string" ? super.create(templateOrOpts, opts) : super.create(templateOrOpts);
  }

  /** Connect to a sandbox, resuming it if paused (e2b's `Sandbox.connect`), with the capacity wait. */
  static override connect<S extends typeof E2BSandbox>(
    this: S,
    sandboxId: string,
    opts?: AnonaSandboxConnectOpts,
  ): Promise<InstanceType<S>> {
    return super.connect(sandboxId, opts) as Promise<InstanceType<S>>;
  }

  /** Connect to this sandbox, resuming it if paused, with the capacity wait. */
  override connect(opts?: AnonaSandboxConnectOpts): Promise<this> {
    return withCapacityWait("sandbox connect", opts, () => super.connect(opts));
  }

  protected static override createSandbox(
    template: string,
    timeoutMs?: number,
    opts?: AnonaSandboxOpts,
  ): Promise<CreateResponse> {
    return withCapacityWait("sandbox create", opts, () => super.createSandbox(template, timeoutMs, opts));
  }

  protected static override connectSandbox(sandboxId: string, opts?: AnonaSandboxConnectOpts): Promise<CreateResponse> {
    return withCapacityWait("sandbox connect", opts, () => super.connectSandbox(sandboxId, opts));
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
