// Serve a port inside a sandbox and open it through a signed preview link.
import { Sandbox } from "@anona-labs/compute-sdk";

const api = "https://api.compute.anonalabs.com";
const key = process.env.ANONA_API_KEY!;

const sbx = await Sandbox.create("python", { timeoutMs: 300_000 });
try {
  await sbx.commands.run("python3 -m http.server 3000 --bind 127.0.0.1", { background: true });
  await new Promise((r) => setTimeout(r, 2000));

  const res = await fetch(`${api}/v1/sandboxes/${sbx.sandboxId}/preview-links`, {
    method: "POST",
    headers: { "X-API-Key": key, "Content-Type": "application/json" },
    body: JSON.stringify({ port: 3000, ttlSeconds: 900 }),
  });
  const link = (await res.json()) as { url: string; expiresAt: string };
  console.log("preview link:", link.url.split("?")[0], "expires", link.expiresAt);

  // The first response sets a cookie and redirects; send that cookie on the next request.
  const first = await fetch(link.url, { redirect: "manual" });
  const cookie = (first.headers.get("set-cookie") ?? "").split(";")[0];
  const page = await fetch(first.headers.get("location") ?? link.url, { headers: { cookie } });
  console.log("status", page.status);
} finally {
  await sbx.kill();
}
