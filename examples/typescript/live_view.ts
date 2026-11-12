// Get a live-view link to a sandbox's terminal.
import { createInterface } from "node:readline/promises";
import { Sandbox } from "@anona-labs/compute-sdk";

const sbx = await Sandbox.create({ timeoutMs: 300_000 });
try {
  const url = await sbx.live.url({ mode: "pty", scope: "control", ttl: 300 }); // a terminal you can type into
  console.log("open this in a browser within 5 minutes:", url);
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  await rl.question("press Enter to delete the sandbox ");
  rl.close();
} finally {
  await sbx.kill();
}
