import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

import { AuthenticationError, NotFoundError } from "e2b";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { runners } from "../src/index.js";
import { API_KEY } from "./fake-gateway.js";

const JOB = {
  id: "j1", repo: "acme/app", workflow: "ci", jobName: "build", label: "anona-small", size: "small",
  status: "running", reason: "", sandboxId: "sbx1", queuedAt: "2026-11-21T10:00:00Z",
  startedAt: "2026-11-21T10:00:05Z", finishedAt: null, conclusion: null,
  githubUrl: "https://github.com/acme/app/actions/runs/1/job/2",
};

let server: Server;
let paths: string[] = [];
const VARS = ["E2B_API_KEY", "E2B_DOMAIN", "E2B_API_URL", "ANONA_API_KEY", "ANONA_DOMAIN", "ANONA_API_URL"];
let saved: Record<string, string | undefined> = {};

beforeEach(async () => {
  saved = Object.fromEntries(VARS.map((v) => [v, process.env[v]]));
  for (const v of VARS) delete process.env[v];
  paths = [];
  server = createServer((req, res) => {
    paths.push(`${req.method} ${req.url}`);
    const send = (code: number, body: unknown) => {
      res.writeHead(code, { "Content-Type": "application/json" });
      res.end(JSON.stringify(body));
    };
    if (req.headers["x-api-key"] !== API_KEY) return send(401, { message: "bad key" });
    const path = (req.url ?? "").split("?")[0];
    if (path === "/v1/runners/installations")
      return send(200, {
        eligible: true, reason: "", allowedSizes: ["small", "medium"],
        installations: [{ id: 12345, accountLogin: "acme", accountType: "Organization", suspended: false, linkedAt: "2026-11-21T10:00:00Z" }],
      });
    if (path === "/v1/runners/jobs") return send(200, { jobs: [JOB] });
    if (path === "/v1/runners/jobs/j1") return send(200, JOB);
    if (path === "/v1/runners/installations/link")
      return send(403, { code: 403, error_code: "runners_not_eligible", message: "runners need a paid plan" });
    return send(404, { message: "not found" });
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  process.env.ANONA_API_URL = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
  process.env.ANONA_API_KEY = API_KEY;
});

afterEach(async () => {
  for (const v of VARS) {
    if (saved[v] === undefined) delete process.env[v];
    else process.env[v] = saved[v];
  }
  await new Promise((r) => server.close(r));
});

describe("runners", () => {
  it("lists installations", async () => {
    const out = await runners.listInstallations();
    expect(out.eligible).toBe(true);
    expect(out.allowedSizes).toEqual(["small", "medium"]);
    expect(out.installations[0]?.accountLogin).toBe("acme");
  });

  it("sends query parameters and maps githubUrl", async () => {
    const jobs = await runners.listJobs({ status: "running", limit: 5 });
    expect(paths.at(-1)).toBe("GET /v1/runners/jobs?status=running&limit=5");
    expect(jobs[0]?.githubUrl).toMatch(/\/job\/2$/);
    await runners.listJobs({ repo: "acme/app" });
    expect(paths.at(-1)).toBe("GET /v1/runners/jobs?repo=acme%2Fapp&limit=20");
  });

  it("gets one job; unknown is NotFoundError", async () => {
    expect((await runners.getJob("j1")).id).toBe("j1");
    await expect(runners.getJob("nope")).rejects.toBeInstanceOf(NotFoundError);
  });

  it("rejects 401 with AuthenticationError", async () => {
    process.env.ANONA_API_KEY = "e2b_" + "00".repeat(20);
    await expect(runners.listJobs()).rejects.toBeInstanceOf(AuthenticationError);
  });

  it("surfaces the not-eligible message from linkUrl", async () => {
    await expect(runners.linkUrl()).rejects.toThrow(/runners need a paid plan/);
  });
});
