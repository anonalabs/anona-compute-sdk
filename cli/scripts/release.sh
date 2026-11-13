#!/usr/bin/env bash
# Cross-compile the anona-compute CLI for every release platform, check that each
# binary carries the version, pack the release archives and write their SHA-256
# checksums. The cli-publish workflow in github.com/anonalabs/anona-compute-sdk runs
# exactly this; it also runs locally and needs only Go, tar, gzip and zip.
#
# Usage: cli/scripts/release.sh [VERSION]
#   VERSION is X.Y.Z (a leading v or cli-v is dropped). Without it the version is
#   taken from the newest cli-v* tag, else 0.0.0; that is for local builds only.
# Output, in cli/dist/:
#   anona-compute_<version>_<os>_<arch>.tar.gz   (.zip for windows), each holding the
#                                                single binary anona-compute[.exe]
#   SHA256SUMS                                   sha256 of every archive
set -euo pipefail

cd "$(dirname "$0")/.."

version="${1:-$(git describe --tags --match 'cli-v*' --abbrev=0 2>/dev/null || echo 0.0.0)}"
# The binary reports, and npm uses, X.Y.Z: drop the tag prefix so
# `anona-compute version`, the archive names and the published version agree.
version="${version#cli-}"
version="${version#v}"
if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "version must look like X.Y.Z, got: $version" >&2
  exit 1
fi
targets=(linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64)
hostos="$(go env GOHOSTOS)" hostarch="$(go env GOHOSTARCH)"

rm -rf dist
mkdir -p dist/bin

for t in "${targets[@]}"; do
  os="${t%/*}"
  arch="${t#*/}"
  name="anona-compute_${version}_${os}_${arch}"
  bin=anona-compute
  [ "$os" = windows ] && bin=anona-compute.exe
  stage="dist/bin/$name"
  mkdir -p "$stage"
  echo "building $name"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -buildvcs=false \
    -ldflags "-s -w -X main.version=${version}" -o "$stage/$bin" ./cmd/anona-compute
  # Every binary must carry the version before anything is packed, and the one
  # this machine can run is also executed: 0.1.3 shipped binaries that reported 0.1.0.
  if [ "$os" = "$hostos" ] && [ "$arch" = "$hostarch" ]; then
    npm/scripts/check-binary-version.sh "$version" "$stage/$bin" run
  else
    npm/scripts/check-binary-version.sh "$version" "$stage/$bin"
  fi
  # Fixed owner, order and timestamps, so the same source gives the same archive.
  # 1980-01-02 UTC, because a zip cannot hold a date before 1980.
  touch -d @315619200 "$stage/$bin"
  if [ "$os" = windows ]; then
    (cd "$stage" && TZ=UTC zip -X -q "../../$name.zip" "$bin")
  else
    tar -C "$stage" --sort=name --owner=0 --group=0 --numeric-owner --mtime=@315619200 -cf - "$bin" \
      | gzip -n > "dist/$name.tar.gz"
  fi
done

(cd dist && sha256sum anona-compute_* > SHA256SUMS)
echo "wrote dist/SHA256SUMS"
cat dist/SHA256SUMS
