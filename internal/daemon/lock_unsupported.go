//go:build !linux && !darwin

package daemon

import (
	"errors"
	"os"
)

var ErrLockHeld = errors.New("daemon lock is held")

type FileLock struct{}

func TryAcquireLock(string) (*FileLock, error) {
	return nil, errors.New("daemon locking is supported only on Linux and macOS")
}

func AdoptLockFile(file *os.File) (*FileLock, error) {
	if file != nil {
		_ = file.Close()
	}
	return nil, errors.New("daemon locking is supported only on Linux and macOS")
}

func (*FileLock) File() *os.File       { return nil }
func (*FileLock) Close() error         { return nil }
func (*FileLock) DropReference() error { return nil }
