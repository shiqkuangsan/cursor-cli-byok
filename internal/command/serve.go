package command

import (
	"context"
	"errors"
	"fmt"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/buildinfo"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
)

func (a App) runServe(args []string) int {
	backgroundChild := false
	switch {
	case len(args) == 0:
	case len(args) == 1 && args[0] == "--background-child":
		backgroundChild = true
	default:
		return usageExitCode
	}
	runtimePaths, err := paths.Resolve(a.Getenv)
	if err != nil {
		return a.fail(err)
	}
	lockPath := daemon.LockPath(runtimePaths)
	var lock *daemon.FileLock
	if backgroundChild {
		lock, err = daemon.AdoptInheritedLock(a.Getenv, lockPath)
	} else {
		lock, err = daemon.TryAcquireLock(lockPath)
		if errors.Is(err, daemon.ErrLockHeld) {
			return a.fail(errors.New("serve: daemon is already running or starting"))
		}
	}
	if err != nil {
		return a.fail(fmt.Errorf("serve: acquire inherited daemon lock: %w", err))
	}
	serviceContext, cancelService := context.WithCancel(a.Context)
	defer cancelService()
	if a.Signals != nil {
		go func() {
			select {
			case <-serviceContext.Done():
			case <-a.Signals:
				cancelService()
			}
		}()
	}
	if err := daemon.RunService(serviceContext, daemon.ServiceOptions{
		Paths:   runtimePaths,
		Version: buildinfo.Version,
		Lock:    lock,
	}); err != nil {
		return a.fail(fmt.Errorf("serve: %w", err))
	}
	return 0
}
