import { createServer, type Server } from "node:http";
import type { AddressInfo } from "node:net";

import { ConnectionConfig } from "e2b";
import { afterEach, beforeEach, expect, it } from "vitest";

import { getJson } from "../src/http.js";

let server: Server;
let base: string;

beforeEach(async () => {
  server = createServer((req, res) => {
    const headers: Record<string, string> = { "Content-Type": "application/json" };
    if (req.url === "/hdr") headers["X-Request-Id"] = "req-hdr";
    res.writeHead(500, headers).end(JSON.stringify({ code: 500, message: "could not start the sandbox", requestId: "req-body" }));
  });
  await new Promise<void>((r) => server.listen(0, "127.0.0.1", r));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}`;
});
afterEach(() => server.close());

it("a gateway error names its request ID, from the header or else the body", async () => {
  const config = new ConnectionConfig({ apiKey: "e2b_x", apiUrl: base } as never);
  await expect(getJson(config, "/hdr")).rejects.toThrow("500: could not start the sandbox (request req-hdr)");
  await expect(getJson(config, "/body")).rejects.toThrow("500: could not start the sandbox (request req-body)");
});
