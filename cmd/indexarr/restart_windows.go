//go:build windows

package main

import "context"

// restartProcess asks the current process to stop after a restore. Windows
// has neither SIGTERM nor a portable way to signal the current process, so it
// cancels the signal context directly: the main loop shuts down gracefully and
// the Windows service manager (or the user) starts indexarr again.
func restartProcess(stop context.CancelFunc) error {
	stop()
	return nil
}
