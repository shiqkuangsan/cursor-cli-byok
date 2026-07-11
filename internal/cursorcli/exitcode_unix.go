//go:build !windows

package cursorcli

import (
	"os/exec"
	"syscall"
)

func normalizedExitCode(exitError *exec.ExitError) int {
	if exitCode := exitError.ExitCode(); exitCode >= 0 {
		return exitCode
	}
	if status, ok := exitError.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return 128 + int(status.Signal())
	}
	return 1
}
