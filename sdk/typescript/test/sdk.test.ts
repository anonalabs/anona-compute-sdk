import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import {
  AuthenticationError,
  ConnectionConfig,
  InvalidArgumentError,
  NotFoundError,
  Sandbox as E2BSandbox,
  type SandboxApiOpts,
} from "e2b";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { DEFAULT_API_URL, Sandbox, applyEnv, configure } from "../src/index.js";
import { API_KEY, FakeGateway, NO_DEADLINE } from "./fake-gateway.js";

const VARS = ["E2B_API_KEY", "E2B_DOMAIN", "E2B_API_URL", "E2B_DEBUG", "ANONA_API_KEY", "ANONA_DOMAIN", "ANONA_API_URL"];
let saved: Record<string, string | undefined> = {};
let gw: FakeGateway;

beforeEach(async () => {
  saved = Object.fromEntries(VARS.map((v) => [v, process.env[v]]));
  for (const v of VARS) delete process.env[v];
  gw = await new FakeGateway().start();
});

afterEach(async () => {
  for (const v of VARS) {
    if (saved[v] === undefined) delete process.env[v];
    else process.env[v] = saved[v];
  }
  await gw.stop();
});

async function create(): Promise<Sandbox> {
  process.env.ANONA_API_KEY = API_KEY;
  return Sandbox.create({ apiUrl: gw.url });
}

function hasHttp(cmd: string): boolean {
  try {
    execFileSync("which", [cmd], { stdio: "ignore" });
    return true;
  } catch {
    return false;
  }
}

describe("config", () => {
  it("defaults to the Anona domain", () => {
    const cfg = new ConnectionConfig(Sandbox.connectionDefaults());
    expect(cfg.domain).toBe("compute.anonalabs.com");
    expect(cfg.apiUrl).toBe(DEFAULT_API_URL);
  });

  it("maps ANONA_API_KEY and lets E2B_* win", () => {
    process.env.ANONA_API_KEY = "e2b_" + "ab".repeat(20);
    expect(new ConnectionConfig(Sandbox.connectionDefaults()).apiKey).toBe("e2b_" + "ab".repeat(20));
    process.env.E2B_API_KEY = "e2b_" + "cd".repeat(20);
    process.env.E2B_DOMAIN = "example.test";
    const cfg = new ConnectionConfig(Sandbox.connectionDefaults());
    expect(cfg.apiKey).toBe("e2b_" + "cd".repeat(20));
    expect(cfg.domain).toBe("example.test");
  });

  it("never sends an ANONA_API_KEY that is not a compute key (an Anona Memory key)", () => {
    process.env.ANONA_API_KEY = "anona_mem_" + "ab".repeat(20);
    expect(Sandbox.connectionDefaults()?.apiKey).toBeUndefined();
    const env: Record<string, string | undefined> = { ANONA_API_KEY: "anona_mem_" + "ab".repeat(20) };
    applyEnv(env);
    expect(env.E2B_API_KEY).toBeUndefined();
    const ok: Record<string, string | undefined> = { ANONA_API_KEY: "ac_" + "ab".repeat(20) };
    applyEnv(ok);
    expect(ok.E2B_API_KEY).toBe("ac_" + "ab".repeat(20));
  });

  it("per-call options win", () => {
    process.env.ANONA_DOMAIN = "a.test";
    expect(new ConnectionConfig(Sandbox.connectionDefaults({ domain: "b.test" })).domain).toBe("b.test");
  });

  it("configure() sets E2B_* so the stock SDK works alone", () => {
    configure({ apiKey: "e2b_" + "ef".repeat(20) });
    expect(process.env.E2B_DOMAIN).toBe("compute.anonalabs.com");
    expect(new ConnectionConfig().apiUrl).toBe("https://api.compute.anonalabs.com");
    const env: Record<string, string | undefined> = { ANONA_API_KEY: "x", E2B_API_KEY: "y" };
    applyEnv(env);
    expect(env.E2B_API_KEY).toBe("y");
  });

  it("create() returns the subclass and sends the key", async () => {
    const sbx = await create();
    expect(sbx).toBeInstanceOf(Sandbox);
    expect(sbx).toBeInstanceOf(E2BSandbox);
    expect(gw.requests[0]?.path).toBe("/v2/sandboxes");
    expect(gw.requests[0]?.headers["x-api-key"]).toBe(API_KEY);
    const again = await Sandbox.connect(sbx.sandboxId, { apiUrl: gw.url });
    expect(again).toBeInstanceOf(Sandbox);
  });
});

// The docs promise `timeoutMs: -1` and `setTimeout(-1)` mean no deadline. The
// gateway's "no deadline" is a timeout of -1 SECONDS; e2b converts with
// Math.ceil(ms / 1000), which turns -1 ms into -0, sent as 0: a create the
// gateway refuses and a setTimeout that ends the sandbox at once.
describe("timeout -1 means no deadline", () => {
  const timeoutOf = (path: string) => (gw.requests.filter((r) => r.path === path).at(-1)?.body as { timeout?: number })?.timeout;
  const endAt = async (id: string) => (await Sandbox.getInfo(id, api())).endAt.toISOString();
  const noDeadline = new Date(NO_DEADLINE).toISOString();
  // SandboxApiOpts leaves apiUrl out of its type, though every call honours it.
  const api = () => ({ apiUrl: gw.url }) as SandboxApiOpts;

  it("create({ timeoutMs: -1 })", async () => {
    process.env.ANONA_API_KEY = API_KEY;
    const sbx = await Sandbox.create({ apiUrl: gw.url, timeoutMs: -1 });
    expect(timeoutOf("/v2/sandboxes")).toBe(-1);
    expect(await endAt(sbx.sandboxId)).toBe(noDeadline);
  });

  it("create(template, { timeoutMs: -1 })", async () => {
    process.env.ANONA_API_KEY = API_KEY;
    const sbx = await Sandbox.create("base", { apiUrl: gw.url, timeoutMs: -1 });
    expect(timeoutOf("/v2/sandboxes")).toBe(-1);
    expect(await endAt(sbx.sandboxId)).toBe(noDeadline);
  });

  it("sandbox.setTimeout(-1) and Sandbox.setTimeout(id, -1)", async () => {
    const sbx = await create();
    await sbx.setTimeout(-1);
    expect(timeoutOf(`/sandboxes/${sbx.sandboxId}/timeout`)).toBe(-1);
    expect(await endAt(sbx.sandboxId)).toBe(noDeadline);

    await sbx.setTimeout(60_000);
    expect(await endAt(sbx.sandboxId)).not.toBe(noDeadline);
    await Sandbox.setTimeout(sbx.sandboxId, -1, api());
    expect(timeoutOf(`/sandboxes/${sbx.sandboxId}/timeout`)).toBe(-1);
    expect(await endAt(sbx.sandboxId)).toBe(noDeadline);
  });

  it("connect({ timeoutMs: -1 }), static and instance", async () => {
    const sbx = await create();
    const path = `/v2/sandboxes/${sbx.sandboxId}/connect`;
    await Sandbox.connect(sbx.sandboxId, { apiUrl: gw.url, timeoutMs: -1 });
    expect(timeoutOf(path)).toBe(-1);
    await sbx.setTimeout(60_000);
    await sbx.connect({ timeoutMs: -1 });
    expect(timeoutOf(path)).toBe(-1);
    expect(await endAt(sbx.sandboxId)).toBe(noDeadline);
  });

  it("fork({ timeoutMs: -1 }), static and instance", async () => {
    const sbx = await create();
    const path = `/sandboxes/${sbx.sandboxId}/fork`;
    const [a] = await sbx.fork({ timeoutMs: -1 });
    expect(timeoutOf(path)).toBe(-1);
    expect(await endAt((a as Sandbox).sandboxId)).toBe(noDeadline);
    const [b] = await Sandbox.fork(sbx.sandboxId, { ...api(), timeoutMs: -1 });
    expect(timeoutOf(path)).toBe(-1);
    expect(b).toBeInstanceOf(Sandbox);
  });

  it("leaves every other timeout to e2b's conversion", async () => {
    const sbx = await create();
    const path = `/sandboxes/${sbx.sandboxId}/timeout`;
    await sbx.setTimeout(3_600_000);
    expect(timeoutOf(path)).toBe(3600);
    await sbx.setTimeout(1_500);
    expect(timeoutOf(path)).toBe(2);
    await sbx.setTimeout(0);
    expect(timeoutOf(path)).toBe(0);
  });

  it("still takes -1000, which meant no deadline before -1 did", async () => {
    const sbx = await create();
    await sbx.setTimeout(60_000);
    await sbx.setTimeout(-1000);
    expect(timeoutOf(`/sandboxes/${sbx.sandboxId}/timeout`)).toBe(-1);
    expect(await endAt(sbx.sandboxId)).toBe(noDeadline);
  });

  it.each([-2, -500, -999, -1001, -1.5, Number.NEGATIVE_INFINITY])("refuses timeoutMs %d before sending", async (ms) => {
    const sbx = await create();
    const before = gw.requests.length;
    await expect(sbx.setTimeout(ms)).rejects.toBeInstanceOf(InvalidArgumentError);
    await expect(Sandbox.setTimeout(sbx.sandboxId, ms, api())).rejects.toBeInstanceOf(InvalidArgumentError);
    await expect(Sandbox.create({ apiUrl: gw.url, timeoutMs: ms })).rejects.toBeInstanceOf(InvalidArgumentError);
    await expect(sbx.connect({ timeoutMs: ms })).rejects.toBeInstanceOf(InvalidArgumentError);
    await expect(sbx.fork({ timeoutMs: ms })).rejects.toBeInstanceOf(InvalidArgumentError);
    expect(gw.requests.length).toBe(before);
  });
});

describe("live", () => {
  it("mints a token whose claims match the request", async () => {
    const sbx = await create();
    const tok = await sbx.live.token({ scope: "control", channels: ["pty", "dom"], ttl: 120 });
    const c = tok.claims();
    expect(c.ch).toEqual(["pty", "dom"]);
    expect(c.scp).toBe("control");
    expect(c.sbx).toBe(sbx.sandboxId);
    expect(c.exp).toBeLessThanOrEqual(c.iat + 600);
    expect(c.exp - c.iat).toBe(120);
    const last = gw.requests.at(-1);
    expect(last?.path).toBe(`/v1/sandboxes/${sbx.sandboxId}/viewer-tokens`);
    expect(last?.body).toEqual({ scope: "control", channels: ["pty", "dom"], ttlSeconds: 120 });
    expect(JSON.stringify(tok)).not.toContain(tok.token);
    expect(String(tok)).not.toContain(tok.token);
  });

  it("url() defaults to a view-only DOM embed URL", async () => {
    const sbx = await create();
    const url = await sbx.live.url();
    expect(url.startsWith(`https://stream.compute.anonalabs.com/embed/v1/${sbx.sandboxId}?mode=dom#t=`)).toBe(true);
    expect(gw.requests.at(-1)?.body).toEqual({ scope: "view", channels: ["dom"], ttlSeconds: 600 });
  });

  it("desktopUrl() mints a control-scoped desktop token", async () => {
    const sbx = await create();
    const url = await sbx.live.desktopUrl({ ttl: 120 });
    expect(url).toContain("#t=");
    expect(gw.requests.at(-1)?.body).toEqual({ scope: "control", channels: ["desktop"], ttlSeconds: 120 });
    await sbx.live.desktopUrl({ control: false });
    expect(gw.requests.at(-1)?.body).toEqual({ scope: "view", channels: ["desktop"], ttlSeconds: 600 });
  });

  it("revokeAll()", async () => {
    const sbx = await create();
    await sbx.live.revokeAll();
    expect(gw.revoked).toEqual([sbx.sandboxId]);
  });

  it.each([{ scope: "admin" }, { channels: [] }, { channels: ["audio"] }, { ttl: 601 }, { ttl: 29 }, { ttl: 60.5 }])(
    "rejects %o before sending",
    async (opts) => {
      const sbx = await create();
      const before = gw.requests.length;
      await expect(sbx.live.token(opts as never)).rejects.toBeInstanceOf(InvalidArgumentError);
      expect(gw.requests.length).toBe(before);
    },
  );

  it("maps gateway errors", async () => {
    const sbx = await create();
    const other = await Sandbox.connect(sbx.sandboxId, { apiUrl: gw.url, apiKey: "e2b_" + "00".repeat(20) }).catch((e) => e);
    expect(other).toBeInstanceOf(AuthenticationError);
    gw.sandboxes.clear();
    await expect(sbx.live.token()).rejects.toBeInstanceOf(NotFoundError);
  });
});

describe("ssh", () => {
  const PUB = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOrq9Z5n3jYbqK1x0aHk0yJ6l1m1nq3o9g2Yx5c7zQ0x me@host";

  it("requests a certificate", async () => {
    const sbx = await create();
    const cert = await sbx.ssh.certificate(PUB);
    expect(cert.cert.startsWith("ssh-ed25519-cert-v01@openssh.com ")).toBe(true);
    expect(cert).toMatchObject({ user: "user", host: sbx.sandboxId, proxyCommand: `anona-compute ssh --proxy ${sbx.sandboxId}` });
    expect(gw.requests.at(-1)?.body).toEqual({ publicKey: PUB });
  });

  it("refuses private keys and garbage without a request", async () => {
    const sbx = await create();
    const before = gw.requests.length;
    await expect(sbx.ssh.certificate("-----BEGIN OPENSSH PRIVATE KEY-----")).rejects.toBeInstanceOf(InvalidArgumentError);
    await expect(sbx.ssh.certificate("not a key")).rejects.toBeInstanceOf(InvalidArgumentError);
    expect(gw.requests.length).toBe(before);
  });

  it.runIf(hasHttp("ssh-keygen"))("signs a real ssh-keygen key", async () => {
    const dir = mkdtempSync(join(tmpdir(), "anona-ssh-"));
    execFileSync("ssh-keygen", ["-q", "-t", "ed25519", "-N", "", "-f", join(dir, "k")]);
    const sbx = await create();
    await sbx.ssh.certificate(readFileSync(join(dir, "k.pub"), "utf8"));
    expect((gw.requests.at(-1)?.body as { publicKey: string }).publicKey).toBe(readFileSync(join(dir, "k.pub"), "utf8").trim());
  });

  it.runIf(hasHttp("ssh-keygen"))("signs ECDSA P-384 and P-521 keys, which the gateway accepts", async () => {
    const sbx = await create();
    for (const bits of ["384", "521"]) {
      const dir = mkdtempSync(join(tmpdir(), "anona-ssh-"));
      execFileSync("ssh-keygen", ["-q", "-t", "ecdsa", "-b", bits, "-N", "", "-f", join(dir, "k")]);
      await sbx.ssh.certificate(readFileSync(join(dir, "k.pub"), "utf8"));
      expect((gw.requests.at(-1)?.body as { publicKey: string }).publicKey.startsWith(`ecdsa-sha2-nistp${bits} `)).toBe(true);
    }
  });

  it.runIf(hasHttp("ssh"))("config() parses with real OpenSSH", async () => {
    const sbx = await create();
    const dir = mkdtempSync(join(tmpdir(), "anona-ssh-"));
    writeFileSync(join(dir, "config"), sbx.ssh.config({ identityFile: join(dir, "k") }));
    const out = execFileSync("ssh", ["-G", "-F", join(dir, "config"), `anona-${sbx.sandboxId}`], { encoding: "utf8" });
    expect(out).toContain(`hostname ${sbx.sandboxId}`);
    expect(out).toContain("user user");
    expect(out).toContain("proxycommand anona-compute ssh --proxy %h");
    expect(out).toContain(`certificatefile ${join(dir, "k")}-cert.pub`);
  });
});
