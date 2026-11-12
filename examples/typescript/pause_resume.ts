// Pause a sandbox (memory and disk are kept) and resume it later.
import { Sandbox } from "@anona-labs/compute-sdk";

const sbx = await Sandbox.create({ timeoutMs: 300_000 });
try {
  await sbx.commands.run("echo hello > /tmp/state.txt");
  await sbx.pause();
  console.log("paused", sbx.sandboxId);

  const again = await Sandbox.connect(sbx.sandboxId); // connecting resumes a paused sandbox
  console.log("after resume:", (await again.commands.run("cat /tmp/state.txt")).stdout.trim());
} finally {
  await Sandbox.kill(sbx.sandboxId);
}
