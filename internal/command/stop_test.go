package command

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
)

func TestStopHandlesStoppedStaleHealthyAndUnhealthyDaemon(t *testing.T) {
	t.Run("already stopped", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		if exitCode := app.Run([]string{"stop"}); exitCode != 0 || stdout.String() != "daemon already stopped\n" {
			t.Fatalf("exit/stdout/stderr = %d/%q/%q", exitCode, stdout.String(), stderr.String())
		}
	})

	t.Run("remove stale state", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		saveOperationalState(t, app.Getenv)
		app.ProcessAlive = func(int) bool { return false }
		if exitCode := app.Run([]string{"stop"}); exitCode != 0 {
			t.Fatalf("stop exit = %d stderr=%q", exitCode, stderr.String())
		}
		if !strings.Contains(stdout.String(), "removed stale daemon state") {
			t.Fatalf("stdout = %q", stdout.String())
		}
		runtimePaths, _ := paths.Resolve(app.Getenv)
		if _, err := daemon.NewStateStore(daemon.StatePath(runtimePaths)).Load(); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Load(state) error = %v", err)
		}
	})

	t.Run("healthy", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		state := saveOperationalState(t, app.Getenv)
		alive := true
		app.ProcessAlive = func(pid int) bool { return pid == state.PID && alive }
		app.DaemonProbe = daemon.ProbeFunc(func(context.Context, daemon.State) error { return nil })
		signals := 0
		app.SignalProcess = func(pid int, signal os.Signal) error {
			if pid != state.PID || signal != syscall.SIGTERM {
				t.Fatalf("signal = pid %d signal %v", pid, signal)
			}
			signals++
			alive = false
			runtimePaths, _ := paths.Resolve(app.Getenv)
			return daemon.NewStateStore(daemon.StatePath(runtimePaths)).Remove()
		}
		if exitCode := app.Run([]string{"stop"}); exitCode != 0 {
			t.Fatalf("stop exit = %d stderr=%q", exitCode, stderr.String())
		}
		if signals != 1 || stdout.String() != "daemon stopped\n" {
			t.Fatalf("signals/stdout = %d/%q", signals, stdout.String())
		}
	})

	t.Run("state removed before PID is reaped", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		state := saveOperationalState(t, app.Getenv)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		app.Context = ctx
		app.ProcessAlive = func(pid int) bool { return pid == state.PID }
		app.DaemonProbe = daemon.ProbeFunc(func(context.Context, daemon.State) error { return nil })
		app.SignalProcess = func(pid int, signal os.Signal) error {
			if pid != state.PID || signal != syscall.SIGTERM {
				t.Fatalf("signal = pid %d signal %v", pid, signal)
			}
			runtimePaths, _ := paths.Resolve(app.Getenv)
			if err := daemon.NewStateStore(daemon.StatePath(runtimePaths)).Remove(); err != nil {
				return err
			}
			cancel()
			return nil
		}
		if exitCode := app.Run([]string{"stop"}); exitCode != 0 {
			t.Fatalf("stop exit = %d stderr=%q", exitCode, stderr.String())
		}
		if stdout.String() != "daemon stopped\n" {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})

	t.Run("preserve replacement daemon state", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		state := saveOperationalState(t, app.Getenv)
		replacement := state
		replacement.PID = state.PID + 1
		replacement.Port = state.Port + 1
		replacement.InstanceID = "abcdef0123456789abcdef0123456789"
		replacement.StartedAt = state.StartedAt.Add(time.Second)
		oldAlive := true
		app.ProcessAlive = func(pid int) bool { return pid == state.PID && oldAlive }
		app.DaemonProbe = daemon.ProbeFunc(func(context.Context, daemon.State) error { return nil })
		app.SignalProcess = func(pid int, signal os.Signal) error {
			if pid != state.PID || signal != syscall.SIGTERM {
				t.Fatalf("signal = pid %d signal %v", pid, signal)
			}
			oldAlive = false
			runtimePaths, _ := paths.Resolve(app.Getenv)
			return daemon.NewStateStore(daemon.StatePath(runtimePaths)).Save(replacement)
		}
		if exitCode := app.Run([]string{"stop"}); exitCode != 0 {
			t.Fatalf("stop exit = %d stderr=%q", exitCode, stderr.String())
		}
		runtimePaths, _ := paths.Resolve(app.Getenv)
		got, err := daemon.NewStateStore(daemon.StatePath(runtimePaths)).Load()
		if err != nil {
			t.Fatalf("Load(replacement) error = %v", err)
		}
		if got.InstanceID != replacement.InstanceID || got.PID != replacement.PID {
			t.Fatalf("replacement state = %#v, want instance %s PID %d", got, replacement.InstanceID, replacement.PID)
		}
	})

	t.Run("preserve replacement published during stale check", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		state := saveOperationalState(t, app.Getenv)
		replacement := state
		replacement.PID = state.PID + 1
		replacement.Port = state.Port + 1
		replacement.InstanceID = "123456789abcdef0123456789abcdef0"
		replacement.StartedAt = state.StartedAt.Add(time.Second)
		runtimePaths, _ := paths.Resolve(app.Getenv)
		store := daemon.NewStateStore(daemon.StatePath(runtimePaths))
		app.ProcessAlive = func(pid int) bool {
			if pid != state.PID {
				return false
			}
			if err := store.Save(replacement); err != nil {
				t.Fatalf("Save(replacement) error = %v", err)
			}
			return false
		}
		app.SignalProcess = func(int, os.Signal) error {
			t.Fatal("stale target was signaled")
			return nil
		}
		if exitCode := app.Run([]string{"stop"}); exitCode != 0 {
			t.Fatalf("stop exit = %d stderr=%q", exitCode, stderr.String())
		}
		got, err := store.Load()
		if err != nil {
			t.Fatalf("Load(replacement) error = %v", err)
		}
		if got.InstanceID != replacement.InstanceID || got.PID != replacement.PID {
			t.Fatalf("replacement state = %#v, want instance %s PID %d", got, replacement.InstanceID, replacement.PID)
		}
	})

	t.Run("preserve replacement published after wait identity check", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		state := saveOperationalState(t, app.Getenv)
		replacement := state
		replacement.PID = state.PID + 1
		replacement.Port = state.Port + 1
		replacement.InstanceID = "fedcba9876543210fedcba9876543210"
		replacement.StartedAt = state.StartedAt.Add(time.Second)
		runtimePaths, _ := paths.Resolve(app.Getenv)
		store := daemon.NewStateStore(daemon.StatePath(runtimePaths))
		aliveChecks := 0
		app.ProcessAlive = func(pid int) bool {
			if pid != state.PID {
				return false
			}
			aliveChecks++
			if aliveChecks == 1 {
				return true
			}
			if err := store.Save(replacement); err != nil {
				t.Fatalf("Save(replacement) error = %v", err)
			}
			return false
		}
		app.DaemonProbe = daemon.ProbeFunc(func(context.Context, daemon.State) error { return nil })
		app.SignalProcess = func(pid int, signal os.Signal) error {
			if pid != state.PID || signal != syscall.SIGTERM {
				t.Fatalf("signal = pid %d signal %v", pid, signal)
			}
			return nil
		}
		if exitCode := app.Run([]string{"stop"}); exitCode != 0 {
			t.Fatalf("stop exit = %d stderr=%q", exitCode, stderr.String())
		}
		got, err := store.Load()
		if err != nil {
			t.Fatalf("Load(replacement) error = %v", err)
		}
		if got.InstanceID != replacement.InstanceID || got.PID != replacement.PID {
			t.Fatalf("replacement state = %#v, want instance %s PID %d", got, replacement.InstanceID, replacement.PID)
		}
	})

	t.Run("refuse unhealthy", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		saveOperationalState(t, app.Getenv)
		app.ProcessAlive = func(int) bool { return true }
		app.DaemonProbe = daemon.ProbeFunc(func(context.Context, daemon.State) error { return errors.New("instance mismatch") })
		app.SignalProcess = func(int, os.Signal) error {
			t.Fatal("unhealthy process was signaled")
			return nil
		}
		if exitCode := app.Run([]string{"stop"}); exitCode == 0 {
			t.Fatal("unhealthy stop exit = 0")
		}
		if !strings.Contains(stderr.String(), "refusing") {
			t.Fatalf("stderr = %q", stderr.String())
		}
	})
}
