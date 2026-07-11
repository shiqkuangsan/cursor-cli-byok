//go:build windows

package cursorcli

import "os/exec"

func normalizedExitCode(exitError *exec.ExitError) int {
	if exitCode := exitError.ExitCode(); exitCode >= 0 {
		return exitCode
	}
	return 1
}
