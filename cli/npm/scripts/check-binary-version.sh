#!/usr/bin/env bash
# Refuse to publish a platform binary that does not carry the version being
# published. 0.1.3 shipped binaries built from the 0.1.0 release, which
# reported 0.1.0: nothing compared the two.
#
# Usage: npm/scripts/check-binary-version.sh VERSION BINARY [run]
#   With "run" the binary is executed (`BINARY version` must print exactly
#   VERSION); use it for the platform the runner can execute. Otherwise the
#   version string is looked for in the binary: the Go linker stores the
#   -X main.version value as a plain string literal (the -ldflags themselves
#   are not in the build info when -trimpath is set, so `go version -m` cannot
#   tell). Neighbouring digits, dots and letters are excluded so that 0.1.3
#   does not match 0.1.30 or a dependency's v0.1.3.
set -euo pipefail

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: $0 VERSION BINARY [run]" >&2
  exit 2
fi
version=$1
bin=$2
mode=${3:-}

if ! printf '%s\n' "$version" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$'; then
  echo "version must look like X.Y.Z, got: $version" >&2
  exit 1
fi
if [ ! -s "$bin" ]; then
  echo "$bin: missing or empty" >&2
  exit 1
fi

if [ "$mode" = run ]; then
  got=$("$bin" version)
  if [ "$got" != "$version" ]; then
    echo "$bin reports version '$got', expected '$version'" >&2
    exit 1
  fi
  echo "$bin: version $got (executed)"
  exit 0
fi

escaped=${version//./\\.}
if ! LC_ALL=C grep -aqE "(^|[^0-9A-Za-z.])${escaped}($|[^0-9A-Za-z.+-])" "$bin"; then
  echo "$bin does not contain the version string $version" >&2
  exit 1
fi
echo "$bin: contains version string $version"
