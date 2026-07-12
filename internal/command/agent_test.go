package command

import (
	"bytes"
	"context"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/certs"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
	localserver "github.com/shiqkuangsan/cursor-cli-byok/internal/server"
)

func TestProviderSecretEnvironmentKeysReturnsUniqueConfiguredNames(t *testing.T) {
	cfg := config.Config{Models: []config.Model{
		{Name: "one", APIKeyEnv: "PROVIDER_KEY_ONE"},
		{Name: "inline", APIKey: "inline-secret"},
		{Name: "duplicate", APIKeyEnv: "PROVIDER_KEY_ONE"},
		{Name: "two", APIKeyEnv: "PROVIDER_KEY_TWO"},
	}}
	want := []string{"PROVIDER_KEY_ONE", "PROVIDER_KEY_TWO"}
	if got := providerSecretEnvironmentKeys(cfg); !reflect.DeepEqual(got, want) {
		t.Fatalf("providerSecretEnvironmentKeys() = %#v, want %#v", got, want)
	}
}

func TestSelectedProviderEnvironmentValuesContainsOnlySelectedEnvironmentKey(t *testing.T) {
	cfg := config.Config{Models: []config.Model{
		{Name: "one", APIKeyEnv: "PROVIDER_KEY_ONE"},
		{Name: "two", APIKeyEnv: "PROVIDER_KEY_TWO"},
		{Name: "inline", APIKey: "inline-secret"},
	}}
	values := selectedProviderEnvironmentValues(cfg, "two", "resolved-secret")
	want := map[string]string{"PROVIDER_KEY_TWO": "resolved-secret"}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("selectedProviderEnvironmentValues() = %#v, want %#v", values, want)
	}
	if got := selectedProviderEnvironmentValues(cfg, "inline", "inline-secret"); len(got) != 0 {
		t.Fatalf("inline values = %#v, want empty", got)
	}
}

func TestRunAgentMissingConfigurationNamesRecoveryBeforeDaemonStartup(t *testing.T) {
	home := t.TempDir()
	var stderr bytes.Buffer
	executableCalled := false
	app := App{
		Context: context.Background(),
		Stderr:  &stderr,
		Getenv:  commandEnv(map[string]string{"HOME": home}),
		Executable: func() (string, error) {
			executableCalled = true
			return filepath.Join(home, "cursor-cli-byok"), nil
		},
	}

	exitCode := app.Run([]string{"-p", "hello"})

	if exitCode == 0 {
		t.Fatal("Run() exit code = 0, want missing configuration failure")
	}
	if executableCalled {
		t.Fatal("Run() located the daemon executable before rejecting missing configuration")
	}
	if !strings.Contains(stderr.String(), "cursor-cli-byok config init") {
		t.Fatalf("stderr = %q, want configuration recovery command", stderr.String())
	}
}

func TestRunAgentSynchronizesSelectedProviderEnvironmentBeforeCursorLaunch(t *testing.T) {
	home := t.TempDir()
	binDirectory := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDirectory, 0o700); err != nil {
		t.Fatalf("MkdirAll(bin) error = %v", err)
	}
	cursorPath := filepath.Join(binDirectory, "cursor-agent")
	cursorScript := `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '%s\n' '2026.07.08-test'
  exit 0
fi
if [ -n "${RELAY_API_KEY+x}" ]; then
  exit 91
fi
exit 0
`
	if err := os.WriteFile(cursorPath, []byte(cursorScript), 0o700); err != nil {
		t.Fatalf("WriteFile(cursor-agent) error = %v", err)
	}

	environmentValues := map[string]string{
		"HOME":          home,
		"PATH":          binDirectory,
		"RELAY_API_KEY": "rotated-secret",
	}
	runtimePaths, err := paths.Resolve(commandEnv(environmentValues))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	cfg := config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay",
		Models: []config.Model{{
			Name: "relay", Protocol: config.ProtocolOpenAI, BaseURL: "https://provider.example.com",
			Endpoint: config.EndpointResponses, APIKeyEnv: "RELAY_API_KEY", UpstreamModel: "upstream",
		}},
	}
	if err := config.NewStore(runtimePaths.ConfigFile).Save(cfg); err != nil {
		t.Fatalf("Save(config) error = %v", err)
	}

	providerEnvironment := daemon.NewProviderEnvironment(func(name string) string {
		if name == "RELAY_API_KEY" {
			return "old-secret"
		}
		return ""
	})
	bundle, err := (certs.Manager{Directory: filepath.Join(runtimePaths.DataDir, "certs")}).Ensure()
	if err != nil {
		t.Fatalf("Ensure(certs) error = %v", err)
	}
	state := daemon.State{
		Version:       daemon.CurrentStateVersion,
		PID:           os.Getpid(),
		CACertPath:    bundle.CACertPath,
		DaemonVersion: "dev",
		InstanceID:    "0123456789abcdef0123456789abcdef",
		AuthToken:     "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjdXJzb3ItY2xpLWJ5b2sifQ.c2lnbmF0dXJl",
		StartedAt:     time.Now().UTC(),
	}
	controlHandler := daemon.NewProviderEnvironmentHandler(func() (config.Config, error) { return cfg, nil }, providerEnvironment.Update)
	serverContext, cancelServer := context.WithCancel(context.Background())
	t.Cleanup(cancelServer)
	running, err := localserver.Start(serverContext, localserver.Options{
		Certificate:   bundle.Certificate,
		InstanceID:    state.InstanceID,
		AuthToken:     state.AuthToken,
		DaemonVersion: state.DaemonVersion,
		Handler:       controlHandler,
	})
	if err != nil {
		t.Fatalf("Start(server) error = %v", err)
	}
	t.Cleanup(func() {
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = running.Shutdown(shutdownContext)
	})
	endpoint, err := url.Parse(running.EndpointURL())
	if err != nil {
		t.Fatalf("Parse(endpoint) error = %v", err)
	}
	state.Port, err = strconv.Atoi(endpoint.Port())
	if err != nil {
		t.Fatalf("Atoi(port) error = %v", err)
	}
	if err := daemon.NewStateStore(daemon.StatePath(runtimePaths)).Save(state); err != nil {
		t.Fatalf("Save(state) error = %v", err)
	}

	var stderr bytes.Buffer
	app := App{
		Context: context.Background(),
		Stderr:  &stderr,
		Getenv:  commandEnv(environmentValues),
		Environ: func() []string {
			return []string{
				"HOME=" + home,
				"PATH=" + binDirectory,
				"RELAY_API_KEY=rotated-secret",
			}
		},
		Executable: func() (string, error) { return filepath.Join(home, "cursor-cli-byok"), nil },
	}
	if exitCode := app.Run([]string{"--model", "relay", "--print", "hello"}); exitCode != 0 {
		t.Fatalf("Run() exit code = %d, stderr = %q", exitCode, stderr.String())
	}
	if got := providerEnvironment.Getenv("RELAY_API_KEY"); got != "rotated-secret" {
		t.Fatalf("daemon provider environment = %q, want rotated secret", got)
	}
	if strings.Contains(stderr.String(), "rotated-secret") {
		t.Fatal("command output leaked provider secret")
	}
	if !strings.Contains(stderr.String(), "warning: cursor-agent version 2026.07.08-test is untested") {
		t.Fatalf("stderr = %q, want untested Cursor version warning", stderr.String())
	}
}
