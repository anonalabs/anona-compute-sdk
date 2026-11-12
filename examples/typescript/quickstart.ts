// Create a sandbox with the stock e2b SDK and run a command in it.
import { Sandbox } from "e2b"; // reads E2B_DOMAIN and E2B_API_KEY

const sbx = await Sandbox.create({ timeoutMs: 300_000 });
try {
  const result = await sbx.commands.run("uname -sr && nproc");
  console.log(result.stdout);
} finally {
  await sbx.kill();
}
