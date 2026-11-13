//go:build windows

package cli

import (
	"errors"
	"os"
	"os/exec"
)

// execReplace: Windows has no process-image replacement, so run path as a
// child with inherited stdio and translate its exit code the same way the
// non-replace path in runTool does.
func execReplace(path string, args, env []string) error {
	cmd := exec.Command(path, args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return silentExit(ee.ExitCode())
		}
		return err
	}
	return nil
}

// notifyWinch is a no-op: Windows has no terminal resize signal.
func notifyWinch(ch chan os.Signal) {}
