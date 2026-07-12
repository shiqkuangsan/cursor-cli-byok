package daemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

type Probe interface {
	Check(context.Context, State) error
}

type ProbeFunc func(context.Context, State) error

func (f ProbeFunc) Check(ctx context.Context, state State) error {
	return f(ctx, state)
}

type Child interface {
	PID() int
	Stop(context.Context) error
	Detach() error
}

type Starter interface {
	Start(context.Context, *os.File) (Child, error)
}

type StarterFunc func(context.Context, *os.File) (Child, error)

func (f StarterFunc) Start(ctx context.Context, lockFile *os.File) (Child, error) {
	return f(ctx, lockFile)
}

type Manager struct {
	Store           StateStore
	LockPath        string
	Probe           Probe
	Starter         Starter
	ExpectedVersion string
	Shutdown        func(context.Context, State) error
	StartTimeout    time.Duration
	StopTimeout     time.Duration
	PollInterval    time.Duration
}

func (m Manager) Ensure(ctx context.Context) (State, error) {
	if err := m.validate(); err != nil {
		return State{}, err
	}
	if ctx == nil {
		return State{}, errors.New("ensure daemon: context is required")
	}
	startupContext, cancel := context.WithTimeout(ctx, m.StartTimeout)
	defer cancel()
	replacementRequestedFor := ""

	for {
		if state, healthy := m.healthyState(startupContext); healthy {
			if m.versionCompatible(state) {
				return state, nil
			}
			if replacementRequestedFor != state.InstanceID {
				if err := m.Shutdown(startupContext, state); err != nil {
					return State{}, fmt.Errorf("ensure daemon: stop incompatible version: %w", err)
				}
				replacementRequestedFor = state.InstanceID
			}
			if err := waitInterval(startupContext, m.PollInterval); err != nil {
				return State{}, m.startupWaitError(err)
			}
			continue
		}
		lock, err := TryAcquireLock(m.LockPath)
		if err == nil {
			return m.startUnderLock(startupContext, lock)
		}
		if !errors.Is(err, ErrLockHeld) {
			return State{}, fmt.Errorf("ensure daemon: %w", err)
		}
		if err := waitInterval(startupContext, m.PollInterval); err != nil {
			return State{}, m.startupWaitError(err)
		}
	}
}

func (m Manager) startUnderLock(ctx context.Context, lock *FileLock) (State, error) {
	releaseWithUnlock := true
	defer func() {
		if releaseWithUnlock {
			_ = lock.Close()
		}
	}()

	if state, healthy := m.healthyState(ctx); healthy {
		if m.versionCompatible(state) {
			return state, nil
		}
		return State{}, errors.New("ensure daemon: incompatible daemon became healthy while startup lock was held")
	}
	if err := m.Store.Remove(); err != nil {
		return State{}, fmt.Errorf("ensure daemon: clear stale state: %w", err)
	}
	child, err := m.Starter.Start(ctx, lock.File())
	if err != nil {
		return State{}, fmt.Errorf("ensure daemon: start: %w", err)
	}
	if child == nil || child.PID() < 1 {
		return State{}, errors.New("ensure daemon: starter returned an invalid child")
	}

	state, err := m.waitForChild(ctx, child.PID())
	if err != nil {
		stopError := m.stopChild(child)
		if stopError != nil {
			return State{}, errors.Join(err, fmt.Errorf("stop failed daemon: %w", stopError))
		}
		return State{}, err
	}
	if err := lock.DropReference(); err != nil {
		_ = m.stopChild(child)
		return State{}, fmt.Errorf("ensure daemon: transfer lock ownership: %w", err)
	}
	releaseWithUnlock = false
	if err := child.Detach(); err != nil {
		detachError := fmt.Errorf("ensure daemon: detach background child: %w", err)
		if stopError := m.stopChild(child); stopError != nil {
			return State{}, errors.Join(detachError, fmt.Errorf("stop undetached daemon: %w", stopError))
		}
		return State{}, detachError
	}
	return state, nil
}

func (m Manager) stopChild(child Child) error {
	timeout := m.StopTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return child.Stop(ctx)
}

func (m Manager) waitForChild(ctx context.Context, pid int) (State, error) {
	for {
		if state, healthy := m.healthyState(ctx); healthy && state.PID == pid && m.versionCompatible(state) {
			return state, nil
		}
		if err := waitInterval(ctx, m.PollInterval); err != nil {
			return State{}, m.startupWaitError(err)
		}
	}
}

func (m Manager) healthyState(ctx context.Context) (State, bool) {
	state, err := m.Store.Load()
	if err != nil || !ProcessAlive(state.PID) {
		return State{}, false
	}
	if err := m.Probe.Check(ctx, state); err != nil {
		return State{}, false
	}
	return state, true
}

func (m Manager) validate() error {
	if m.Store.path == "" {
		return errors.New("ensure daemon: state store is required")
	}
	if m.LockPath == "" {
		return errors.New("ensure daemon: lock path is required")
	}
	if m.Probe == nil {
		return errors.New("ensure daemon: health probe is required")
	}
	if m.Starter == nil {
		return errors.New("ensure daemon: starter is required")
	}
	if m.ExpectedVersion != "" {
		if m.ExpectedVersion != strings.TrimSpace(m.ExpectedVersion) || containsControl(m.ExpectedVersion) {
			return errors.New("ensure daemon: expected version is invalid")
		}
		if m.Shutdown == nil {
			return errors.New("ensure daemon: version-aware shutdown is required")
		}
	}
	if m.StartTimeout <= 0 {
		return errors.New("ensure daemon: positive startup timeout is required")
	}
	if m.PollInterval <= 0 {
		return errors.New("ensure daemon: positive poll interval is required")
	}
	return nil
}

func (m Manager) versionCompatible(state State) bool {
	return m.ExpectedVersion == "" || state.DaemonVersion == m.ExpectedVersion
}

func (m Manager) startupWaitError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("ensure daemon: startup timed out after %s", m.StartTimeout)
	}
	return fmt.Errorf("ensure daemon: startup canceled: %w", err)
}

func waitInterval(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
