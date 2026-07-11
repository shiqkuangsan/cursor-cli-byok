//go:build linux || darwin

package daemon

import (
	"os"
	"os/exec"
	"syscall"
)

func configureDetached(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func killDetached(process *os.Process) error {
	return syscall.Kill(-process.Pid, syscall.SIGKILL)
}
