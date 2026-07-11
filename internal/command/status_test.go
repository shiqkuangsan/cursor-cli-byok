package command

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
)

func TestStatusReportsStoppedRunningAndStale(t *testing.T) {
	t.Run("stopped", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		if exitCode := app.Run([]string{"status"}); exitCode != 0 {
			t.Fatalf("status exit = %d stderr=%q", exitCode, stderr.String())
		}
		if stdout.String() != "daemon: stopped\n" {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})

	t.Run("running", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		state := saveOperationalState(t, app.Getenv)
		app.ProcessAlive = func(pid int) bool { return pid == state.PID }
		app.DaemonProbe = daemon.ProbeFunc(func(context.Context, daemon.State) error { return nil })
		if exitCode := app.Run([]string{"status"}); exitCode != 0 {
			t.Fatalf("status exit = %d stderr=%q", exitCode, stderr.String())
		}
		for _, want := range []string{"daemon: running\n", "pid: 123\n", "endpoint: https://127.0.0.1:43123\n", "version: dev\n"} {
			if !strings.Contains(stdout.String(), want) {
				t.Fatalf("stdout = %q, missing %q", stdout.String(), want)
			}
		}
	})

	t.Run("dead process", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		saveOperationalState(t, app.Getenv)
		app.ProcessAlive = func(int) bool { return false }
		if exitCode := app.Run([]string{"status"}); exitCode == 0 {
			t.Fatal("stale status exit = 0")
		}
		if !strings.Contains(stdout.String(), "daemon: stale") || !strings.Contains(stdout.String(), "process is not running") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})

	t.Run("unhealthy", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		app := operationalTestApp(t, &stdout, &stderr)
		saveOperationalState(t, app.Getenv)
		app.ProcessAlive = func(int) bool { return true }
		app.DaemonProbe = daemon.ProbeFunc(func(context.Context, daemon.State) error { return errors.New("TLS mismatch") })
		if exitCode := app.Run([]string{"status"}); exitCode == 0 {
			t.Fatal("unhealthy status exit = 0")
		}
		if !strings.Contains(stdout.String(), "daemon: stale") || !strings.Contains(stdout.String(), "health check failed") {
			t.Fatalf("stdout = %q", stdout.String())
		}
	})
}

func operationalTestApp(t *testing.T, stdout, stderr *bytes.Buffer) App {
	t.Helper()
	home := t.TempDir()
	values := map[string]string{
		"HOME":            home,
		"XDG_CONFIG_HOME": filepath.Join(home, "config"),
		"XDG_DATA_HOME":   filepath.Join(home, "data"),
		"XDG_STATE_HOME":  filepath.Join(home, "state"),
	}
	return App{
		Context: context.Background(), Stdout: stdout, Stderr: stderr,
		Getenv: commandEnv(values), ProcessAlive: daemon.ProcessAlive,
		DaemonProbe: daemon.HTTPProbe{Timeout: 100 * time.Millisecond},
	}
}

func saveOperationalState(t *testing.T, getenv func(string) string) daemon.State {
	t.Helper()
	runtimePaths, err := paths.Resolve(getenv)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	state := daemon.State{
		Version: daemon.CurrentStateVersion, PID: 123, Port: 43123,
		CACertPath: filepath.Join(t.TempDir(), "ca.pem"), DaemonVersion: "dev",
		InstanceID: "0123456789abcdef0123456789abcdef",
		AuthToken:  "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjdXJzb3ItY2xpLWJ5b2sifQ.c2lnbmF0dXJl",
		StartedAt:  time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC),
	}
	if err := daemon.NewStateStore(daemon.StatePath(runtimePaths)).Save(state); err != nil {
		t.Fatalf("Save(state) error = %v", err)
	}
	return state
}
