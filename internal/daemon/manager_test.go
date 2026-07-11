package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestManagerReusesHealthyDaemon(t *testing.T) {
	directory := t.TempDir()
	store := NewStateStore(filepath.Join(directory, "daemon.json"))
	want := validState()
	want.PID = os.Getpid()
	if err := store.Save(want); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	var starts atomic.Int32
	manager := Manager{
		Store:    store,
		LockPath: filepath.Join(directory, "daemon.lock"),
		Probe:    ProbeFunc(func(context.Context, State) error { return nil }),
		Starter: StarterFunc(func(context.Context, *os.File) (Child, error) {
			starts.Add(1)
			return nil, errors.New("unexpected start")
		}),
		StartTimeout: time.Second,
		PollInterval: 10 * time.Millisecond,
	}

	got, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if got != want {
		t.Fatalf("Ensure() = %#v, want %#v", got, want)
	}
	if starts.Load() != 0 {
		t.Fatalf("starter calls = %d, want 0", starts.Load())
	}
}

func TestManagerReplacesHealthyDaemonWithDifferentExpectedVersion(t *testing.T) {
	directory := t.TempDir()
	store := NewStateStore(filepath.Join(directory, "daemon.json"))
	oldState := validState()
	oldState.PID = os.Getpid()
	oldState.DaemonVersion = "v0.1.0"
	if err := store.Save(oldState); err != nil {
		t.Fatalf("Save(old state) error = %v", err)
	}
	want := oldState
	want.DaemonVersion = "v0.2.0"
	want.Port++
	want.InstanceID = "abcdef0123456789abcdef0123456789"
	var shutdowns atomic.Int32
	var starts atomic.Int32
	manager := Manager{
		Store:           store,
		LockPath:        filepath.Join(directory, "daemon.lock"),
		Probe:           ProbeFunc(func(context.Context, State) error { return nil }),
		ExpectedVersion: "v0.2.0",
		Shutdown: func(_ context.Context, state State) error {
			shutdowns.Add(1)
			if state != oldState {
				return errors.New("unexpected daemon selected for replacement")
			}
			return store.Remove()
		},
		Starter: StarterFunc(func(context.Context, *os.File) (Child, error) {
			starts.Add(1)
			if err := store.Save(want); err != nil {
				return nil, err
			}
			return &fakeChild{pid: want.PID}, nil
		}),
		StartTimeout: time.Second,
		StopTimeout:  time.Second,
		PollInterval: 5 * time.Millisecond,
	}

	got, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if got != want || shutdowns.Load() != 1 || starts.Load() != 1 {
		t.Fatalf("Ensure() = %#v, shutdowns=%d starts=%d, want %#v/1/1", got, shutdowns.Load(), starts.Load(), want)
	}
}

func TestManagerReplacesStaleStateAndPollsUntilHealthy(t *testing.T) {
	directory := t.TempDir()
	store := NewStateStore(filepath.Join(directory, "daemon.json"))
	stale := validState()
	stale.PID = 1 << 30
	if err := store.Save(stale); err != nil {
		t.Fatalf("Save(stale) error = %v", err)
	}
	want := validState()
	want.PID = os.Getpid()
	want.Port = 43125
	want.InstanceID = "abcdef0123456789abcdef0123456789"
	var probes atomic.Int32
	child := &fakeChild{pid: want.PID}
	manager := Manager{
		Store:    store,
		LockPath: filepath.Join(directory, "daemon.lock"),
		Probe: ProbeFunc(func(_ context.Context, state State) error {
			if state.InstanceID != want.InstanceID || probes.Add(1) < 3 {
				return errors.New("not ready")
			}
			return nil
		}),
		Starter: StarterFunc(func(context.Context, *os.File) (Child, error) {
			go func() {
				time.Sleep(20 * time.Millisecond)
				_ = store.Save(want)
			}()
			return child, nil
		}),
		StartTimeout: time.Second,
		PollInterval: 10 * time.Millisecond,
	}

	got, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	if got != want {
		t.Fatalf("Ensure() = %#v, want %#v", got, want)
	}
	if probes.Load() < 3 {
		t.Fatalf("probe calls = %d, want at least 3", probes.Load())
	}
	if child.stopCalls.Load() != 0 {
		t.Fatalf("Stop() calls = %d, want 0", child.stopCalls.Load())
	}
	if child.detachCalls.Load() != 1 {
		t.Fatalf("Detach() calls = %d, want 1", child.detachCalls.Load())
	}
}

func TestManagerConcurrentEnsureStartsDaemonOnce(t *testing.T) {
	directory := t.TempDir()
	store := NewStateStore(filepath.Join(directory, "daemon.json"))
	want := validState()
	want.PID = os.Getpid()
	want.InstanceID = "fedcba9876543210fedcba9876543210"
	var starts atomic.Int32
	manager := Manager{
		Store:        store,
		LockPath:     filepath.Join(directory, "daemon.lock"),
		Probe:        ProbeFunc(func(context.Context, State) error { return nil }),
		StartTimeout: 2 * time.Second,
		PollInterval: 5 * time.Millisecond,
	}
	manager.Starter = StarterFunc(func(context.Context, *os.File) (Child, error) {
		starts.Add(1)
		time.Sleep(30 * time.Millisecond)
		if err := store.Save(want); err != nil {
			return nil, err
		}
		return &fakeChild{pid: want.PID}, nil
	})

	const wrappers = 8
	results := make(chan State, wrappers)
	errorsChannel := make(chan error, wrappers)
	var group sync.WaitGroup
	for range wrappers {
		group.Add(1)
		go func() {
			defer group.Done()
			state, err := manager.Ensure(context.Background())
			if err != nil {
				errorsChannel <- err
				return
			}
			results <- state
		}()
	}
	group.Wait()
	close(results)
	close(errorsChannel)
	for err := range errorsChannel {
		t.Fatalf("Ensure() error = %v", err)
	}
	for state := range results {
		if state != want {
			t.Fatalf("Ensure() state = %#v, want %#v", state, want)
		}
	}
	if starts.Load() != 1 {
		t.Fatalf("starter calls = %d, want 1", starts.Load())
	}
}

func TestManagerStopsChildAfterStartupTimeout(t *testing.T) {
	directory := t.TempDir()
	child := &fakeChild{pid: os.Getpid()}
	manager := Manager{
		Store:        NewStateStore(filepath.Join(directory, "daemon.json")),
		LockPath:     filepath.Join(directory, "daemon.lock"),
		Probe:        ProbeFunc(func(context.Context, State) error { return errors.New("not ready") }),
		Starter:      StarterFunc(func(context.Context, *os.File) (Child, error) { return child, nil }),
		StartTimeout: 60 * time.Millisecond,
		PollInterval: 10 * time.Millisecond,
	}

	_, err := manager.Ensure(context.Background())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Ensure() error = %v, want startup timeout", err)
	}
	if child.stopCalls.Load() != 1 {
		t.Fatalf("Stop() calls = %d, want 1", child.stopCalls.Load())
	}
	if child.detachCalls.Load() != 0 {
		t.Fatalf("Detach() calls = %d, want 0", child.detachCalls.Load())
	}
}

func TestExecStarterLaunchesBackgroundChildWithInheritedLockAndSanitizedEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix descriptor inheritance is required")
	}
	directory := t.TempDir()
	outputPrefix := filepath.Join(directory, "child")
	executable := filepath.Join(directory, "cursor-cli-byok")
	script := `#!/bin/sh
printf '%s\n' "$@" > "$TEST_OUTPUT.args"
env | sort > "$TEST_OUTPUT.env"
if [ -e "/proc/$$/fd/$CURSOR_CLI_BYOK_LOCK_FD" ] || [ -e "/dev/fd/$CURSOR_CLI_BYOK_LOCK_FD" ]; then
  printf inherited > "$TEST_OUTPUT.lock"
fi
while :; do sleep 1; done
`
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	lockPath := filepath.Join(directory, "daemon.lock")
	lock, err := TryAcquireLock(lockPath)
	if err != nil {
		t.Fatalf("TryAcquireLock() error = %v", err)
	}
	starter := ExecStarter{
		Executable:            executable,
		ProviderSecretEnvKeys: []string{"RELAY_API_KEY", "CURSOR_CLI_BYOK_PROVIDER_KEY"},
		ParentEnv: []string{
			"PATH=/bin:/usr/bin",
			"HOME=" + directory,
			"TEST_OUTPUT=" + outputPrefix,
			"RELAY_API_KEY=provider-secret",
			"CURSOR_CLI_BYOK_PROVIDER_KEY=provider-secret-prefixed",
			"CURSOR_AUTH_TOKEN=cursor-secret",
			"CURSOR_API_ENDPOINT=https://api2.cursor.sh",
			"CURSOR_LOCAL_AGENT_BASE_URL=https://other.example.com",
			"CURSOR_CLI_BYOK_LOCK_FD=99",
		},
	}
	child, err := starter.Start(context.Background(), lock.File())
	if err != nil {
		_ = lock.Close()
		t.Fatalf("Start() error = %v", err)
	}
	defer child.Stop(context.Background())
	if err := lock.DropReference(); err != nil {
		t.Fatalf("DropReference() error = %v", err)
	}

	for _, suffix := range []string{".args", ".env", ".lock"} {
		waitForFile(t, outputPrefix+suffix, 3*time.Second)
	}
	argsData, err := os.ReadFile(outputPrefix + ".args")
	if err != nil {
		t.Fatalf("ReadFile(args) error = %v", err)
	}
	if got := string(argsData); got != "serve\n--background-child\n" {
		t.Fatalf("child args = %q, want serve background child", got)
	}
	environmentData, err := os.ReadFile(outputPrefix + ".env")
	if err != nil {
		t.Fatalf("ReadFile(env) error = %v", err)
	}
	environment := string(environmentData)
	for _, want := range []string{"RELAY_API_KEY=provider-secret", "CURSOR_CLI_BYOK_PROVIDER_KEY=provider-secret-prefixed", "CURSOR_CLI_BYOK_LOCK_FD=3"} {
		if !strings.Contains(environment, want) {
			t.Fatalf("child environment missing %q: %s", want, environment)
		}
	}
	for _, forbidden := range []string{"cursor-secret", "CURSOR_API_ENDPOINT", "CURSOR_LOCAL_AGENT_BASE_URL", "CURSOR_CLI_BYOK_LOCK_FD=99"} {
		if strings.Contains(environment, forbidden) {
			t.Fatalf("child environment contains forbidden value %q", forbidden)
		}
	}
	if _, err := TryAcquireLock(lockPath); !errors.Is(err, ErrLockHeld) {
		t.Fatalf("TryAcquireLock(while child running) error = %v, want ErrLockHeld", err)
	}
}

type fakeChild struct {
	pid         int
	stopCalls   atomic.Int32
	detachCalls atomic.Int32
}

func waitForFile(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (c *fakeChild) PID() int {
	return c.pid
}

func (c *fakeChild) Stop(context.Context) error {
	c.stopCalls.Add(1)
	return nil
}

func (c *fakeChild) Detach() error {
	c.detachCalls.Add(1)
	return nil
}

func TestManagerBoundsFailedChildShutdown(t *testing.T) {
	directory := t.TempDir()
	child := &blockingChild{pid: os.Getpid()}
	manager := Manager{
		Store:        NewStateStore(filepath.Join(directory, "daemon.json")),
		LockPath:     filepath.Join(directory, "daemon.lock"),
		Probe:        ProbeFunc(func(context.Context, State) error { return errors.New("not ready") }),
		Starter:      StarterFunc(func(context.Context, *os.File) (Child, error) { return child, nil }),
		StartTimeout: 30 * time.Millisecond,
		StopTimeout:  40 * time.Millisecond,
		PollInterval: 5 * time.Millisecond,
	}
	startedAt := time.Now()

	_, err := manager.Ensure(context.Background())

	if err == nil || !strings.Contains(err.Error(), "stop failed") {
		t.Fatalf("Ensure() error = %v, want bounded stop failure", err)
	}
	if elapsed := time.Since(startedAt); elapsed > 250*time.Millisecond {
		t.Fatalf("Ensure() elapsed = %s, shutdown was not bounded", elapsed)
	}
}

type blockingChild struct {
	pid int
}

func (c *blockingChild) PID() int { return c.pid }

func (*blockingChild) Stop(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (*blockingChild) Detach() error { return nil }
