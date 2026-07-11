//go:build linux || darwin

package daemon

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

var ErrLockHeld = errors.New("daemon lock is held")

type FileLock struct {
	mu   sync.Mutex
	file *os.File
}

func TryAcquireLock(path string) (*FileLock, error) {
	if path == "" || !filepath.IsAbs(path) {
		return nil, errors.New("acquire daemon lock: path must be absolute")
	}
	directory := filepath.Dir(path)
	if directory == filepath.Clean(path) {
		return nil, errors.New("acquire daemon lock: path must name a file")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("acquire daemon lock: create directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("acquire daemon lock: secure directory permissions: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("acquire daemon lock: lock file must not be a symlink")
		}
		if !info.Mode().IsRegular() {
			return nil, errors.New("acquire daemon lock: lock file must be regular")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("acquire daemon lock: inspect file: %w", err)
	}

	fd, err := syscall.Open(path, syscall.O_RDWR|syscall.O_CREAT|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, fmt.Errorf("acquire daemon lock: open file: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("acquire daemon lock: secure file permissions: %w", err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("acquire daemon lock: flock: %w", err)
	}
	return &FileLock{file: file}, nil
}

func AdoptLockFile(file *os.File) (*FileLock, error) {
	if file == nil {
		return nil, errors.New("adopt daemon lock: file is required")
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("adopt daemon lock: inspect file: %w", err)
	}
	if !info.Mode().IsRegular() {
		_ = file.Close()
		return nil, errors.New("adopt daemon lock: file must be regular")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("adopt daemon lock: secure file permissions: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("adopt daemon lock: flock: %w", err)
	}
	return &FileLock{file: file}, nil
}

func (l *FileLock) File() *os.File {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file
}

func (l *FileLock) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockError := syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
	closeError := file.Close()
	return errors.Join(unlockError, closeError)
}

func (l *FileLock) DropReference() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	return file.Close()
}
