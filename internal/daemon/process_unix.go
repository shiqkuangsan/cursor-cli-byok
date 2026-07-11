//go:build linux || darwin

package daemon

import (
	"errors"
	"syscall"
)

func ProcessAlive(pid int) bool {
	if pid < 1 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
