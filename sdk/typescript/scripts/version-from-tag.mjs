// Sets package.json "version" from the newest git tag sdk-v<version>.
// Without such a tag the version is left unchanged. Run before `pnpm pack`.
import { execFileSync } from "node:child_process";
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const pkgPath = join(dirname(fileURLToPath(import.meta.url)), "..", "package.json");
let tag = "";
try {
  tag = execFileSync("git", ["describe", "--tags", "--abbrev=0", "--match", "sdk-v*"], {
    encoding: "utf8",
    stdio: ["ignore", "pipe", "ignore"],
  }).trim();
} catch {
  // no sdk-v* tag reachable
}
const pkg = JSON.parse(readFileSync(pkgPath, "utf8"));
if (!tag) {
  console.log(`no sdk-v* tag; keeping version ${pkg.version}`);
  process.exit(0);
}
const version = tag.replace(/^sdk-v/, "");
if (!/^\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$/.test(version)) {
  console.error(`tag ${tag} is not sdk-v<semver>`);
  process.exit(1);
}
pkg.version = version;
writeFileSync(pkgPath, JSON.stringify(pkg, null, 2) + "\n");
console.log(`version ${version} from ${tag}`);
