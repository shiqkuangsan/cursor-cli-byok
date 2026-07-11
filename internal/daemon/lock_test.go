package daemon

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
)

func TestTryAcquireLockIsExclusiveAndReusable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock is required")
	}
	path := filepath.Join(t.TempDir(), "state", "daemon.lock")
	first, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("TryAcquireLock(first) error = %v", err)
	}
	defer first.Close()
	assertMode(t, filepath.Dir(path), 0o700)
	assertMode(t, path, 0o600)

	if _, err := TryAcquireLock(path); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("TryAcquireLock(second) error = %v, want ErrLockHeld", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}
	second, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("TryAcquireLock(after close) error = %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("Close(second) error = %v", err)
	}
}

func TestLockReferenceCanTransferToInheritedDescriptor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("flock descriptor inheritance is required")
	}
	path := filepath.Join(t.TempDir(), "state", "daemon.lock")
	parent, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("TryAcquireLock() error = %v", err)
	}
	duplicateFD, err := syscall.Dup(int(parent.File().Fd()))
	if err != nil {
		t.Fatalf("Dup() error = %v", err)
	}
	inherited, err := AdoptLockFile(os.NewFile(uintptr(duplicateFD), "inherited-daemon-lock"))
	if err != nil {
		t.Fatalf("AdoptLockFile() error = %v", err)
	}
	if err := parent.DropReference(); err != nil {
		t.Fatalf("DropReference() error = %v", err)
	}
	if _, err := TryAcquireLock(path); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("TryAcquireLock(while inherited) error = %v, want ErrLockHeld", err)
	}
	if err := inherited.Close(); err != nil {
		t.Fatalf("Close(inherited) error = %v", err)
	}
	reacquired, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("TryAcquireLock(after inherited close) error = %v", err)
	}
	_ = reacquired.Close()
}

func TestTryAcquireLockRejectsSymlinkWithoutChangingTarget(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix symlink behavior is required")
	}
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	target := filepath.Join(directory, "target")
	if err := os.WriteFile(target, []byte("target"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	path := filepath.Join(directory, "daemon.lock")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	_, err := TryAcquireLock(path)
	if err == nil {
		t.Fatal("TryAcquireLock() error = nil, want symlink rejection")
	}
	assertMode(t, target, 0o644)
}

func TestAdoptInheritedLockReadsDescriptorFromEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix descriptor inheritance is required")
	}
	path := filepath.Join(t.TempDir(), "state", "daemon.lock")
	parent, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("TryAcquireLock() error = %v", err)
	}
	duplicateFD, err := syscall.Dup(int(parent.File().Fd()))
	if err != nil {
		t.Fatalf("Dup() error = %v", err)
	}
	inherited, err := AdoptInheritedLock(func(key string) string {
		if key == inheritedLockFDEnvironment {
			return strconv.Itoa(duplicateFD)
		}
		return ""
	}, path)
	if err != nil {
		t.Fatalf("AdoptInheritedLock() error = %v", err)
	}
	if err := parent.DropReference(); err != nil {
		t.Fatalf("DropReference() error = %v", err)
	}
	if _, err := TryAcquireLock(path); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("TryAcquireLock(while inherited) error = %v, want ErrLockHeld", err)
	}
	_ = inherited.Close()
}

func TestAdoptInheritedLockRejectsDescriptorForDifferentFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix descriptor inheritance is required")
	}
	directory := filepath.Join(t.TempDir(), "state")
	actualPath := filepath.Join(directory, "actual.lock")
	expectedPath := filepath.Join(directory, "expected.lock")
	actual, err := TryAcquireLock(actualPath)
	if err != nil {
		t.Fatalf("TryAcquireLock(actual) error = %v", err)
	}
	defer actual.Close()
	expected, err := TryAcquireLock(expectedPath)
	if err != nil {
		t.Fatalf("TryAcquireLock(expected) error = %v", err)
	}
	if err := expected.Close(); err != nil {
		t.Fatalf("Close(expected) error = %v", err)
	}
	duplicateFD, err := syscall.Dup(int(actual.File().Fd()))
	if err != nil {
		t.Fatalf("Dup() error = %v", err)
	}

	_, err = AdoptInheritedLock(func(key string) string {
		if key == inheritedLockFDEnvironment {
			return strconv.Itoa(duplicateFD)
		}
		return ""
	}, expectedPath)
	if err == nil {
		t.Fatal("AdoptInheritedLock() error = nil, want inode mismatch")
	}
}
