import {
  type ConnectionOpts,
  Sandbox as E2BSandbox,
  type SandboxApiOpts,
  type SandboxConnectOpts,
  type SandboxOpts,
} from "e2b";

import { type CapacityWaitOpts, withCapacityWait } from "./capacity.js";
import { anonaDefaults } from "./config.js";
import { IdlePause } from "./idle.js";
import { Live } from "./live.js";
import { Ssh } from "./ssh.js";
import { wireTimeoutMs, withWireTimeout } from "./timeout.js";

/** `Sandbox.create` options: e2b's, plus the capacity wait. */
export type AnonaSandboxOpts = SandboxOpts & CapacityWaitOpts;
/** `Sandbox.connect` options: e2b's, plus the capacity wait. */
export type AnonaSandboxConnectOpts = SandboxConnectOpts & CapacityWaitOpts;

type CreateResponse = Awaited<ReturnType<(typeof E2BSandbox)["createSandbox"]>>;
type ForkResponse = Awaited<ReturnType<(typeof E2BSandbox)["forkSandbox"]>>;

/**
 * The stock e2b `Sandbox` with Anona defaults, plus `live`, `ssh` and
 * `idlePause`. `create` and `connect` wait for capacity: while the fleet is
 * full they retry a `503` marked `Should-Retry: true` after its `Retry-After`,
 * for up to `waitForCapacityMs` (10 minutes by default, `0` turns it off).
 *
 * A `timeoutMs` of `-1` (on `create`, `connect`, `fork` and `setTimeout`) means
 * no deadline, where the team has no maximum lifetime; see `./timeout.ts`.
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
  override async connect(opts?: AnonaSandboxConnectOpts): Promise<this> {
    const wired = withWireTimeout(opts);
    return withCapacityWait("sandbox connect", wired, () => super.connect(wired));
  }

  /** Set this sandbox's timeout (e2b's `setTimeout`); `-1` means no deadline. */
  override async setTimeout(timeoutMs: number, opts?: Pick<SandboxOpts, "requestTimeoutMs" | "signal">): Promise<void> {
    return super.setTimeout(wireTimeoutMs(timeoutMs), opts);
  }

  /** Set a sandbox's timeout (e2b's `Sandbox.setTimeout`); `-1` means no deadline. */
  static override async setTimeout(sandboxId: string, timeoutMs: number, opts?: SandboxApiOpts): Promise<void> {
    return super.setTimeout(sandboxId, wireTimeoutMs(timeoutMs), opts);
  }

  protected static override async createSandbox(
    template: string,
    timeoutMs?: number,
    opts?: AnonaSandboxOpts,
  ): Promise<CreateResponse> {
    const wired = wireTimeoutMs(timeoutMs);
    return withCapacityWait("sandbox create", opts, () => super.createSandbox(template, wired, opts));
  }

  protected static override async connectSandbox(sandboxId: string, opts?: AnonaSandboxConnectOpts): Promise<CreateResponse> {
    const wired = withWireTimeout(opts);
    return withCapacityWait("sandbox connect", wired, () => super.connectSandbox(sandboxId, wired));
  }

  protected static override async forkSandbox(
    sandboxId: string,
    timeoutMs?: number,
    count?: number,
    opts?: SandboxApiOpts,
  ): Promise<ForkResponse> {
    return super.forkSandbox(sandboxId, wireTimeoutMs(timeoutMs), count, opts);
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
