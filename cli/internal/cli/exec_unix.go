//go:build !windows

package cli

import (
	"os"
	"os/signal"
	"syscall"
)

// execReplace replaces the current process image with path, so ssh/scp own
// the terminal, signals and exit status directly.
func execReplace(path string, args, env []string) error {
	return syscall.Exec(path, args, env)
}

// notifyWinch relays terminal resize signals to ch.
func notifyWinch(ch chan os.Signal) {
	signal.Notify(ch, syscall.SIGWINCH)
}
