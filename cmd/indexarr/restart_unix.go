//go:build !windows

package main

import (
	"context"
	"os"
	"syscall"
)

// restartProcess asks the current process to stop after a restore by sending
// it SIGTERM, the same signal systemd and the other supervisors use. The
// signal context then cancels and the main loop shuts down gracefully; a
// supervisor is responsible for starting the process again.
func restartProcess(context.CancelFunc) error {
	return syscall.Kill(os.Getpid(), syscall.SIGTERM)
}
