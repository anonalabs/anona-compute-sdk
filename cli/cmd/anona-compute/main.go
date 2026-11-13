// Command anona-compute is the Anona Compute command-line tool.
package main

import (
	"os"
	"runtime/debug"
	"strings"

	"github.com/anonalabs/anona-compute-sdk/cli/internal/cli"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Main(resolveVersion(version, debug.ReadBuildInfo), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

// resolveVersion keeps a stamped version. An unstamped binary from
// `go install .../cli/cmd/anona-compute@<version>` reports the module version Go
// recorded (v0.3.0 -> 0.3.0, or a pseudo-version for a commit); a plain
// `go build` in a checkout records "(devel)" and stays "dev".
func resolveVersion(stamped string, info func() (*debug.BuildInfo, bool)) string {
	if stamped != "dev" {
		return stamped
	}
	if bi, ok := info(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return strings.TrimPrefix(bi.Main.Version, "v")
	}
	return stamped
}
