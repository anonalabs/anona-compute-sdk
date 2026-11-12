#!/usr/bin/env bash
# Build the Python wheel/sdist and the npm tarball, then install each
# into a fresh Docker image and import it. Publishing stays manual.
# Usage: PYTHON=<python with the build module> sdk/package-check.sh [out-dir]
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$(mktemp -d)}"
mkdir -p "$out"
out="$(cd "$out" && pwd)"
py_image="python:3.12-slim"
node_image="node:22-slim"

echo "== build python (version from git tag sdk-v*)"
"${PYTHON:-python3}" -m build "$root/sdk/python" --outdir "$out"

echo "== pack typescript"
(cd "$root/sdk/typescript" && node scripts/version-from-tag.mjs && pnpm pack --pack-destination "$out" >/dev/null)
ls -l "$out"

echo "== python: clean venv in $py_image"
docker run --rm -v "$out:/pkg:ro" "$py_image" sh -ec '
  python -m venv /tmp/v
  /tmp/v/bin/pip install -q /pkg/*.whl
  /tmp/v/bin/python -c "import anona_compute; print(\"anona-compute\", anona_compute.__version__); print(anona_compute.Sandbox.__mro__[1])"
'

echo "== python: install from sdist in $py_image"
docker run --rm -v "$out:/pkg:ro" "$py_image" sh -ec '
  python -m venv /tmp/v
  /tmp/v/bin/pip install -q /pkg/*.tar.gz
  /tmp/v/bin/python -c "import anona_compute; print(\"anona-compute (sdist)\", anona_compute.__version__)"
'

echo "== typescript: temp project in $node_image"
docker run --rm -v "$out:/pkg:ro" "$node_image" sh -ec '
  mkdir /tmp/p && cd /tmp/p && npm init -y >/dev/null
  npm install --silent --no-audit --no-fund /pkg/anona-labs-compute-sdk-*.tgz e2b@2.61.0
  node --input-type=module -e "const m = await import(\"@anona-labs/compute-sdk\"); console.log(\"@anona-labs/compute-sdk exports:\", Object.keys(m).sort().join(\",\"))"
'
echo "package-check: ok ($out)"
