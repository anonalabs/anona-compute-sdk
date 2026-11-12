/**
 * Test-only fake of the gateway endpoints the SDK extras call: an in-process
 * HTTP server (like Go's httptest.Server) that speaks the wire contract from
 * plan 05 sections 3.4 and 3.7 and records every request. Never used outside tests.
 */
import { createServer, type IncomingMessage, type Server, type ServerResponse } from "node:http";
import type { AddressInfo } from "node:net";

export const API_KEY = "e2b_" + "0123456789abcdef".repeat(2) + "01234567";

export interface Recorded {
  method: string;
  path: string;
  headers: Record<string, string>;
  body: unknown;
}

const b64 = (s: string) => Buffer.from(s).toString("base64url");

export function fakeJws(claims: object): string {
  return [b64(JSON.stringify({ alg: "EdDSA", kid: "test", typ: "anona-view+jwt" })), b64(JSON.stringify(claims)), b64("sig")].join(".");
}

export class FakeGateway {
  readonly requests: Recorded[] = [];
  readonly revoked: string[] = [];
  readonly sandboxes = new Map<string, { templateID: string }>();
  url = "";
  private server: Server;

  constructor() {
    this.server = createServer((req, res) => {
      void this.handle(req, res);
    });
  }

  async start(): Promise<this> {
    await new Promise<void>((resolve) => this.server.listen(0, "127.0.0.1", resolve));
    this.url = `http://127.0.0.1:${(this.server.address() as AddressInfo).port}`;
    return this;
  }

  async stop(): Promise<void> {
    this.server.closeAllConnections();
    await new Promise<void>((resolve) => this.server.close(() => resolve()));
  }

  sandboxJson(id: string) {
    return {
      sandboxID: id,
      templateID: this.sandboxes.get(id)?.templateID ?? "base",
      clientID: "client",
      envdVersion: "0.2.0",
      envdAccessToken: "envd-token",
      domain: "sbx.compute.anonalabs.com",
    };
  }

  private send(res: ServerResponse, code: number, body?: unknown) {
    if (body === undefined) {
      res.writeHead(code).end();
      return;
    }
    res.writeHead(code, { "Content-Type": "application/json" }).end(JSON.stringify(body));
  }

  private async handle(req: IncomingMessage, res: ServerResponse) {
    const chunks: Buffer[] = [];
    for await (const c of req) chunks.push(c as Buffer);
    const raw = Buffer.concat(chunks).toString();
    const body = raw ? (JSON.parse(raw) as Record<string, unknown>) : undefined;
    const path = req.url ?? "/";
    const headers: Record<string, string> = {};
    for (const [k, v] of Object.entries(req.headers)) headers[k] = String(v);
    this.requests.push({ method: req.method ?? "", path, headers, body });

    if (req.method !== "POST") return this.send(res, 405, { code: 405, message: "method not allowed" });
    if (headers["x-api-key"] !== API_KEY) return this.send(res, 401, { code: 401, message: "Invalid API key" });

    const parts = path.replace(/^\/+/, "").split("/");
    if (path === "/v2/sandboxes") {
      const id = `sbx${String(this.sandboxes.size + 1).padStart(4, "0")}`;
      this.sandboxes.set(id, { templateID: String(body?.templateID ?? "base") });
      return this.send(res, 201, this.sandboxJson(id));
    }
    if (parts[0] === "v2" && parts[1] === "sandboxes" && parts[3] === "connect") {
      const id = parts[2] ?? "";
      if (!this.sandboxes.has(id)) return this.send(res, 404, { code: 404, message: "sandbox not found" });
      return this.send(res, 201, this.sandboxJson(id));
    }
    if (parts[0] === "v1" && parts[1] === "sandboxes" && parts.length === 4) {
      const id = decodeURIComponent(parts[2] ?? "");
      const action = parts[3];
      if (!this.sandboxes.has(id)) return this.send(res, 404, { code: 404, message: "sandbox not found" });
      if (action === "viewer-tokens") {
        const now = Math.floor(Date.now() / 1000);
        const ttl = Number(body?.ttlSeconds);
        const channels = body?.channels as string[];
        const token = fakeJws({
          iss: "https://api.compute.anonalabs.com",
          aud: "stream.compute.anonalabs.com",
          sub: "key_test",
          tid: "org_test",
          sbx: id,
          ch: channels,
          scp: body?.scope,
          ep: 1,
          iat: now,
          nbf: now,
          exp: now + ttl,
          jti: "vt_test",
        });
        // The real gateway answers 200 (stream/token/handlers.go).
        return this.send(res, 200, {
          token,
          expiresAt: new Date((now + ttl) * 1000).toISOString(),
          embedUrl: `https://stream.compute.anonalabs.com/embed/v1/${id}?mode=${channels[0]}#t=${token}`,
        });
      }
      if (action === "viewer-tokens:revoke-all") {
        this.revoked.push(id);
        return this.send(res, 200, { epoch: this.revoked.length });
      }
      if (action === "ssh-certs") {
        const key = String(body?.publicKey ?? "");
        // 200, and the proxy command names the sandbox (sshca/handler.go).
        return this.send(res, 200, {
          certificate: "ssh-ed25519-cert-v01@openssh.com AAAAfake " + (key.split(" ")[1] ?? "").slice(0, 8),
          user: "user",
          host: id,
          proxyCommand: `anona-compute ssh --proxy ${id}`,
          expiresAt: new Date(Date.now() + 600_000).toISOString(),
        });
      }
    }
    return this.send(res, 404, { code: 404, message: "not found" });
  }
}
