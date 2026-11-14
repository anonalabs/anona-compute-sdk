// The capacity wait on create and connect, against the fake gateway.
// `retries: 0` turns off the e2b SDK's own short retry, so every 503 reaches
// the wait, and the clock below stands in for real sleeps. The jitter stays
// random except where a test depends on its value.
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { type AnonaSandboxOpts, Sandbox, configure } from "../src/index.js";
import {
  CAPACITY_WAIT_ENV,
  type CapacityWaitInfo,
  clock,
  parseDurationMs,
  resetCapacityWaitDefaults,
  retryDelayMs,
} from "../src/capacity.js";
import { API_KEY, FakeGateway } from "./fake-gateway.js";

const CREATE = "/v2/sandboxes";
const CAPACITY = {
  code: 503,
  error_code: "sandbox_capacity_unavailable",
  message: "no sandbox capacity is free right now; retry in 7s",
};
const RETRY = { "Should-Retry": "true", "Retry-After": "7" };

const VARS = ["E2B_API_KEY", "E2B_DOMAIN", "E2B_API_URL", "ANONA_API_KEY", CAPACITY_WAIT_ENV];
let saved: Record<string, string | undefined> = {};
let gw: FakeGateway;
let now = 0;
let sleeps: number[] = [];
const real = { ...clock };

beforeEach(async () => {
  saved = Object.fromEntries(VARS.map((v) => [v, process.env[v]]));
  for (const v of VARS) delete process.env[v];
  process.env.ANONA_API_KEY = API_KEY;
  resetCapacityWaitDefaults();
  now = 0;
  sleeps = [];
  clock.now = () => now;
  clock.sleep = async (ms: number, signal?: AbortSignal) => {
    if (signal?.aborted) throw signal.reason;
    sleeps.push(ms);
    now += ms;
  };
  gw = await new FakeGateway().start();
});

afterEach(async () => {
  for (const v of VARS) {
    if (saved[v] === undefined) delete process.env[v];
    else process.env[v] = saved[v];
  }
  Object.assign(clock, real);
  resetCapacityWaitDefaults();
  await gw.stop();
});

const create = (opts: AnonaSandboxOpts = {}) =>
  Sandbox.create({ apiUrl: gw.url, retries: 0, ...opts });

describe("capacity wait", () => {
  it("waits out a full fleet, then creates", async () => {
    gw.fail(CREATE, 503, CAPACITY, RETRY, 2);
    const seen: CapacityWaitInfo[] = [];
    const sbx = await create({ onCapacityWait: (i) => seen.push(i) });
    expect(sbx.sandboxId).toBe("sbx0001");
    expect(gw.posts(CREATE)).toBe(3);
    expect(sleeps).toHaveLength(2);
    for (const d of sleeps) expect(d).toBeGreaterThanOrEqual(6_300), expect(d).toBeLessThanOrEqual(7_700);
    expect(seen.map((i) => i.attempt)).toEqual([1, 2]);
    expect(seen[0]?.status).toBe(503);
    expect(String((seen[0]?.error as Error).message)).toContain("no sandbox capacity is free");
  });

  it("holds Retry-After between 1 and 30 seconds", async () => {
    gw.fail(CREATE, 503, CAPACITY, { "Should-Retry": "true", "Retry-After": "600" });
    gw.fail(CREATE, 503, CAPACITY, { "Should-Retry": "true", "Retry-After": "0" });
    gw.fail(CREATE, 503, CAPACITY, { "Should-Retry": "true" });
    await create();
    expect(gw.posts(CREATE)).toBe(4);
    const [big, small, missing] = sleeps;
    expect(big).toBeGreaterThanOrEqual(27_000);
    expect(big).toBeLessThanOrEqual(30_000);
    expect(small).toBeGreaterThanOrEqual(1_000);
    expect(small).toBeLessThanOrEqual(1_100);
    expect(missing).toBeGreaterThanOrEqual(4_500); // no Retry-After: 5 s
    expect(missing).toBeLessThanOrEqual(5_500);
    expect(retryDelayMs("12", () => 0.5)).toBe(12_000);
  });

  // How many tries fit in the wait depends on the jitter, so it is pinned: at
  // the low end a fourth full wait still fits before the deadline, and with
  // Math.random about one run in twelve failed on the count.
  it.each([
    // jitter, the waits (Retry-After 7 s, +-10%), tries
    [0, [6_300, 6_300, 6_300, 1_100], 5], // tries at 0, 6.3, 12.6, 18.9 and 20 s
    [0.5, [7_000, 7_000, 6_000], 4], // tries at 0, 7, 14 and 20 s
    [0.999, [7_699, 7_699, 4_602], 4], // tries at 0, 7.7, 15.4 and 20 s
  ])("throws the original error once the wait runs out (jitter %s)", async (jitter, waits, tries) => {
    clock.random = () => jitter;
    gw.fail(CREATE, 503, CAPACITY, RETRY, 100);
    await expect(create({ waitForCapacityMs: 20_000 })).rejects.toThrow(/no sandbox capacity is free/);
    // every wait but the last is the jittered Retry-After; the last is cut to
    // what is left of the 20 s, and the try after it is the last one
    expect(sleeps).toEqual(waits);
    expect(sleeps.reduce((a, b) => a + b, 0)).toBe(20_000);
    expect(gw.posts(CREATE)).toBe(tries);
  });

  it("is off at 0", async () => {
    gw.fail(CREATE, 503, CAPACITY, RETRY, 5);
    await expect(create({ waitForCapacityMs: 0 })).rejects.toThrow(/Service temporarily unavailable/);
    expect(gw.posts(CREATE)).toBe(1);
    expect(sleeps).toEqual([]);
  });

  it("takes its default from configure() and the environment; the call wins", async () => {
    gw.fail(CREATE, 503, CAPACITY, RETRY, 5);
    process.env[CAPACITY_WAIT_ENV] = "0";
    await expect(create()).rejects.toThrow();
    expect(gw.posts(CREATE)).toBe(1);

    process.env[CAPACITY_WAIT_ENV] = "10m";
    configure({ waitForCapacityMs: 0 }); // configure() wins over the environment
    await expect(create()).rejects.toThrow();
    expect(gw.posts(CREATE)).toBe(2);

    await create({ waitForCapacityMs: 600_000 }); // the call wins over configure()
    expect(gw.posts(CREATE)).toBe(6);
  });

  it.each([
    ["a 403 plan limit", 403, { code: 403, error_code: "plan_limit_exceeded", message: "plan limit" }, { "Should-Retry": "false" }],
    ["a 429 plan limit", 429, { code: 429, error_code: "plan_limit_exceeded", message: "too many" }, { "Should-Retry": "false" }],
    ["a 503 with Should-Retry: false", 503, CAPACITY, { "Should-Retry": "false", "Retry-After": "7" }],
    ["a 503 without Should-Retry", 503, CAPACITY, { "Retry-After": "7" }],
    ["a 500", 500, { code: 500, message: "sandbox engine error" }, {}],
  ])("does not retry %s", async (_name, status, body, headers) => {
    gw.fail(CREATE, status, body, headers, 5);
    await expect(create()).rejects.toThrow();
    expect(gw.posts(CREATE)).toBe(1);
    expect(sleeps).toEqual([]);
  });

  it("stops waiting when the caller's signal aborts", async () => {
    gw.fail(CREATE, 503, CAPACITY, RETRY, 5);
    const ctl = new AbortController();
    await expect(create({ signal: ctl.signal, onCapacityWait: () => ctl.abort(new Error("caller gave up")) })).rejects.toThrow(
      "caller gave up",
    );
    expect(gw.posts(CREATE)).toBe(1);
  });

  it("waits on connect too, static and instance", async () => {
    const sbx = await create();
    const path = `/v2/sandboxes/${sbx.sandboxId}/connect`;
    gw.fail(path, 503, { ...CAPACITY, error_code: "sandbox_engine_starting" }, RETRY);
    const seen: CapacityWaitInfo[] = [];
    const again = await Sandbox.connect(sbx.sandboxId, { apiUrl: gw.url, retries: 0, onCapacityWait: (i) => seen.push(i) });
    expect(again.sandboxId).toBe(sbx.sandboxId);
    expect(again).toBeInstanceOf(Sandbox);
    expect(gw.posts(path)).toBe(2);
    expect(seen).toHaveLength(1);

    gw.fail(path, 503, CAPACITY, RETRY);
    await expect(sbx.connect({ retries: 0 })).resolves.toBe(sbx);
    expect(gw.posts(path)).toBe(4);

    gw.fail(path, 503, CAPACITY, RETRY);
    await expect(sbx.connect({ retries: 0, waitForCapacityMs: 0 })).rejects.toThrow(/Service temporarily unavailable/);
  });

  it("takes over when the e2b SDK's own retries run out", async () => {
    // e2b retries 3 times itself (about 3 s here, real time), then throws.
    gw.fail(CREATE, 503, CAPACITY, { "Should-Retry": "true", "Retry-After": "1" }, 4);
    const sbx = await Sandbox.create({ apiUrl: gw.url });
    expect(sbx.sandboxId).toBe("sbx0001");
    expect(gw.posts(CREATE)).toBe(5);
    expect(sleeps).toHaveLength(1);
  }, 20_000);

  it("keeps concurrent creates apart", async () => {
    gw.fail(CREATE, 503, CAPACITY, { "Should-Retry": "false" });
    gw.fail(CREATE, 503, CAPACITY, RETRY);
    const results = await Promise.allSettled([create(), create()]);
    // One create got the refusal not to retry and failed; the other waited and won.
    expect(results.filter((r) => r.status === "fulfilled")).toHaveLength(1);
    expect(results.filter((r) => r.status === "rejected")).toHaveLength(1);
    expect(sleeps).toHaveLength(1);
  });

  it("parses durations", () => {
    expect(parseDurationMs("600")).toBe(600_000);
    expect(parseDurationMs("10m")).toBe(600_000);
    expect(parseDurationMs("1m30s")).toBe(90_000);
    expect(parseDurationMs("0")).toBe(0);
    expect(parseDurationMs("soon")).toBeUndefined();
  });
});
