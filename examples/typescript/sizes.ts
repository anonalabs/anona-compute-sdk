// Create a sandbox at a chosen size: small, medium, large or xl.
import { Sandbox, withSize } from "@anona-labs/compute-sdk";

const sbx = await Sandbox.create("python", withSize("large", { timeoutMs: 300_000 })); // 4 vCPU, 8 GiB, 40 GB disk
try {
  const result = await sbx.commands.run("nproc && free -g | sed -n 2p && df -h / | tail -1");
  console.log(result.stdout);
} finally {
  await sbx.kill();
}
