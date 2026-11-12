import { Sandbox as E2BSandbox } from "e2b";
import { describe, expect, it } from "vitest";

import { Sandbox } from "../src/index.js";

const url = process.env.GATEWAY_URL;
const key = process.env.GATEWAY_API_KEY;

async function roundtrip(sbx: E2BSandbox) {
  try {
    expect((await sbx.commands.run("echo ok")).stdout.trim()).toBe("ok");
    await sbx.files.write("/tmp/anona.txt", "hello");
    expect(await sbx.files.read("/tmp/anona.txt")).toBe("hello");
  } finally {
    await sbx.kill();
  }
}

describe.runIf(url && key)("live gateway", () => {
  it("anona Sandbox", async () => {
    await roundtrip(await Sandbox.create({ apiUrl: url, apiKey: key }));
  });
  it("stock e2b Sandbox with E2B_* only", async () => {
    await roundtrip(await E2BSandbox.create({ apiUrl: url, apiKey: key }));
  });
});
