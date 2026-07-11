package command

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
)

const daemonStopTimeout = 5 * time.Second

func (a App) runStop(args []string) int {
	if len(args) != 0 {
		return usageExitCode
	}
	runtimePaths, err := paths.Resolve(a.Getenv)
	if err != nil {
		return a.fail(fmt.Errorf("stop: %w", err))
	}
	store := daemon.NewStateStore(daemon.StatePath(runtimePaths))
	lockPath := daemon.LockPath(runtimePaths)
	state, err := store.Load()
	if errors.Is(err, os.ErrNotExist) {
		if _, writeError := fmt.Fprintln(a.Stdout, "daemon already stopped"); writeError != nil {
			return a.fail(errors.New("stop: write output"))
		}
		return 0
	}
	if err != nil {
		return a.fail(errors.New("stop: daemon state is invalid"))
	}
	if !a.ProcessAlive(state.PID) {
		removed, err := removeDaemonStateIfOwned(store, lockPath, state)
		if err != nil {
			return a.fail(fmt.Errorf("stop: %w", err))
		}
		message := "daemon stopped"
		if removed {
			message = "removed stale daemon state"
		}
		if _, err := fmt.Fprintln(a.Stdout, message); err != nil {
			return a.fail(errors.New("stop: write output"))
		}
		return 0
	}
	if err := a.DaemonProbe.Check(a.Context, state); err != nil {
		return a.fail(errors.New("stop: refusing to signal a process that failed the daemon health check"))
	}
	if err := a.SignalProcess(state.PID, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return a.fail(fmt.Errorf("stop: signal daemon: %w", err))
	}
	deadline := time.NewTimer(daemonStopTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		currentState, stateError := store.Load()
		if errors.Is(stateError, os.ErrNotExist) {
			if _, err := fmt.Fprintln(a.Stdout, "daemon stopped"); err != nil {
				return a.fail(errors.New("stop: write output"))
			}
			return 0
		}
		if stateError != nil {
			return a.fail(errors.New("stop: daemon state became invalid while waiting for exit"))
		}
		if currentState.InstanceID != state.InstanceID || currentState.PID != state.PID {
			if _, err := fmt.Fprintln(a.Stdout, "daemon stopped"); err != nil {
				return a.fail(errors.New("stop: write output"))
			}
			return 0
		}
		if !a.ProcessAlive(state.PID) {
			if _, err := removeDaemonStateIfOwned(store, lockPath, state); err != nil {
				return a.fail(fmt.Errorf("stop: remove state: %w", err))
			}
			if _, err := fmt.Fprintln(a.Stdout, "daemon stopped"); err != nil {
				return a.fail(errors.New("stop: write output"))
			}
			return 0
		}
		select {
		case <-a.Context.Done():
			return a.fail(fmt.Errorf("stop: %w", a.Context.Err()))
		case <-deadline.C:
			return a.fail(errors.New("stop: timed out waiting for daemon to exit"))
		case <-ticker.C:
		}
	}
}

func removeDaemonStateIfOwned(store daemon.StateStore, lockPath string, expected daemon.State) (removed bool, err error) {
	lock, err := daemon.TryAcquireLock(lockPath)
	if errors.Is(err, daemon.ErrLockHeld) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() {
		err = errors.Join(err, lock.Close())
	}()
	current, err := store.Load()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if current.InstanceID != expected.InstanceID || current.PID != expected.PID {
		return false, nil
	}
	if err := store.Remove(); err != nil {
		return false, err
	}
	return true, nil
}
