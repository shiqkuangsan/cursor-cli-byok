//go:build !linux && !darwin

package daemon

import (
	"os"
	"os/exec"
)

func configureDetached(*exec.Cmd) {}

func killDetached(process *os.Process) error {
	return process.Kill()
}
