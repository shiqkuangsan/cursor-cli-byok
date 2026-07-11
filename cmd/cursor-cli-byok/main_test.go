package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
)

func TestCommandPrintsLinkerVersion(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() could not locate the test file")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	binaryPath := filepath.Join(t.TempDir(), "cursor-cli-byok")

	build := exec.Command(
		"go", "build",
		"-ldflags", "-X github.com/shiqkuangsan/cursor-cli-byok/internal/buildinfo.Version=v9.8.7",
		"-o", binaryPath,
		"./cmd/cursor-cli-byok",
	)
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}

	output, err := exec.Command(binaryPath, "--version").CombinedOutput()
	if err != nil {
		t.Fatalf("cursor-cli-byok --version failed: %v\n%s", err, output)
	}
	const want = "cursor-cli-byok v9.8.7\n"
	if got := string(output); got != want {
		t.Fatalf("cursor-cli-byok --version output = %q, want %q", got, want)
	}
}

func TestCommandReportsConfigErrorsOnStderr(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() could not locate the test file")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	binaryPath := filepath.Join(t.TempDir(), "cursor-cli-byok")
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/cursor-cli-byok")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}

	home := t.TempDir()
	command := exec.Command(binaryPath, "config", "init", "--non-interactive")
	command.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"),
		"XDG_STATE_HOME="+filepath.Join(home, "state"),
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatal("config init exit error = nil, want invalid configuration failure")
	}
	if got := string(output); got == "" {
		t.Fatal("config init produced no diagnostic output")
	}
}

func TestCommandBackgroundDaemonLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux/macOS daemon lifecycle is required")
	}
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() could not locate the test file")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	binaryPath := filepath.Join(t.TempDir(), "cursor-cli-byok")
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/cursor-cli-byok")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}

	root := t.TempDir()
	runtimePaths := paths.Paths{
		ConfigDir:  filepath.Join(root, "config", "cursor-cli-byok"),
		ConfigFile: filepath.Join(root, "config", "cursor-cli-byok", "config.yaml"),
		DataDir:    filepath.Join(root, "data", "cursor-cli-byok"),
		StateDir:   filepath.Join(root, "state", "cursor-cli-byok"),
	}
	if err := config.NewStore(runtimePaths.ConfigFile).Save(config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay-gpt",
		Models: []config.Model{{
			Name:          "relay-gpt",
			Protocol:      config.ProtocolOpenAI,
			BaseURL:       "https://api.example.com",
			Endpoint:      config.EndpointResponses,
			APIKeyEnv:     "RELAY_API_KEY",
			UpstreamModel: "gpt-5.4",
		}},
	}); err != nil {
		t.Fatalf("Save(config) error = %v", err)
	}
	environment := []string{
		"HOME=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"RELAY_API_KEY=test-provider-key",
	}
	store := daemon.NewStateStore(daemon.StatePath(runtimePaths))
	manager := daemon.Manager{
		Store:        store,
		LockPath:     daemon.LockPath(runtimePaths),
		Probe:        daemon.HTTPProbe{Timeout: time.Second},
		Starter:      daemon.ExecStarter{Executable: binaryPath, ParentEnv: environment},
		StartTimeout: 5 * time.Second,
		StopTimeout:  time.Second,
		PollInterval: 20 * time.Millisecond,
	}
	state, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	t.Cleanup(func() {
		if daemon.ProcessAlive(state.PID) {
			if process, findError := os.FindProcess(state.PID); findError == nil {
				_ = process.Kill()
			}
		}
	})
	reused, err := manager.Ensure(context.Background())
	if err != nil {
		t.Fatalf("Ensure(reuse) error = %v", err)
	}
	if reused.InstanceID != state.InstanceID || reused.PID != state.PID {
		t.Fatalf("Ensure(reuse) = %#v, want same daemon %#v", reused, state)
	}
	process, err := os.FindProcess(state.PID)
	if err != nil {
		t.Fatalf("FindProcess() error = %v", err)
	}
	if err := process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal(SIGTERM) error = %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, stateError := store.Load()
		if errors.Is(stateError, os.ErrNotExist) && !daemon.ProcessAlive(state.PID) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("daemon did not cleanly stop; state error = %v, alive = %t", stateError, daemon.ProcessAlive(state.PID))
		}
		time.Sleep(20 * time.Millisecond)
	}
	lock, err := daemon.TryAcquireLock(daemon.LockPath(runtimePaths))
	if err != nil {
		t.Fatalf("TryAcquireLock(after stop) error = %v", err)
	}
	_ = lock.Close()
}

func TestCommandExplicitWrapperLaunchesCursorAgentAgainstLocalDaemon(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Linux/macOS wrapper process semantics are required")
	}
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller() could not locate the test file")
	}
	moduleRoot := filepath.Clean(filepath.Join(filepath.Dir(testFile), "..", ".."))
	binaryPath := filepath.Join(t.TempDir(), "cursor-cli-byok")
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/cursor-cli-byok")
	build.Dir = moduleRoot
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}

	root := t.TempDir()
	runtimePaths := paths.Paths{
		ConfigDir:  filepath.Join(root, "config", "cursor-cli-byok"),
		ConfigFile: filepath.Join(root, "config", "cursor-cli-byok", "config.yaml"),
		DataDir:    filepath.Join(root, "data", "cursor-cli-byok"),
		StateDir:   filepath.Join(root, "state", "cursor-cli-byok"),
	}
	if err := config.NewStore(runtimePaths.ConfigFile).Save(config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay-gpt",
		Models: []config.Model{{
			Name:          "relay-gpt",
			Protocol:      config.ProtocolOpenAI,
			BaseURL:       "https://api.example.com",
			Endpoint:      config.EndpointResponses,
			APIKeyEnv:     "RELAY_API_KEY",
			UpstreamModel: "gpt-5.4",
		}},
	}); err != nil {
		t.Fatalf("Save(config) error = %v", err)
	}
	fakeBin := filepath.Join(root, "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatalf("Mkdir(fake bin) error = %v", err)
	}
	captureArgs := filepath.Join(root, "cursor-args")
	captureEnv := filepath.Join(root, "cursor-env")
	fakeCursor := filepath.Join(fakeBin, "cursor-agent")
	script := `#!/bin/sh
if [ "${1:-}" = "--version" ]; then
  printf '2026.07.08-0c04a8a\n'
  exit 0
fi
printf '%s\n' "$@" > "$CAPTURE_ARGS"
env | sort > "$CAPTURE_ENV"
exit 23
`
	if err := os.WriteFile(fakeCursor, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(fake cursor-agent) error = %v", err)
	}
	command := exec.Command(binaryPath, "-p", "hello")
	command.Env = []string{
		"HOME=" + root,
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"),
		"XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"PATH=" + fakeBin + ":/usr/bin:/bin",
		"RELAY_API_KEY=provider-secret",
		"CURSOR_API_KEY=must-not-survive",
		"CAPTURE_ARGS=" + captureArgs,
		"CAPTURE_ENV=" + captureEnv,
	}
	output, err := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 23 {
		t.Fatalf("wrapper exit error = %v, output = %s; want cursor-agent exit 23", err, output)
	}
	argsData, err := os.ReadFile(captureArgs)
	if err != nil {
		t.Fatalf("ReadFile(args) error = %v", err)
	}
	argsLines := strings.Split(strings.TrimSpace(string(argsData)), "\n")
	if len(argsLines) != 6 || argsLines[0] != "-e" || !strings.HasPrefix(argsLines[1], "https://127.0.0.1:") || argsLines[2] != "--model" || argsLines[3] != "relay-gpt" || argsLines[4] != "-p" || argsLines[5] != "hello" {
		t.Fatalf("cursor-agent args = %#v, want local endpoint/model plus original args", argsLines)
	}
	environmentData, err := os.ReadFile(captureEnv)
	if err != nil {
		t.Fatalf("ReadFile(env) error = %v", err)
	}
	environment := string(environmentData)
	for _, want := range []string{"CURSOR_AUTH_TOKEN=", "NODE_EXTRA_CA_CERTS=", "CURSOR_API_ENDPOINT=https://127.0.0.1:"} {
		if !strings.Contains(environment, want) {
			t.Fatalf("cursor-agent environment missing %q", want)
		}
	}
	if strings.Contains(environment, "must-not-survive") || strings.Contains(string(output), "provider-secret") {
		t.Fatal("wrapper leaked or forwarded a reserved secret")
	}

	store := daemon.NewStateStore(daemon.StatePath(runtimePaths))
	state, err := store.Load()
	if err != nil {
		t.Fatalf("Load(state) error = %v", err)
	}
	process, err := os.FindProcess(state.PID)
	if err == nil {
		_ = process.Signal(syscall.SIGTERM)
	}
	deadline := time.Now().Add(5 * time.Second)
	for daemon.ProcessAlive(state.PID) && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if daemon.ProcessAlive(state.PID) {
		t.Fatalf("daemon PID %d did not stop", state.PID)
	}
}
