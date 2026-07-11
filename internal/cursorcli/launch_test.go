package cursorcli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestBuildLaunchSpecInjectsLocalSettingsAndPreservesUserArguments(t *testing.T) {
	cursorPath := filepath.Join(t.TempDir(), "cursor-agent")
	caPath := filepath.Join(t.TempDir(), "ca.pem")
	userArgs := []string{"-p", "--output-format", "stream-json", "explain this repository"}

	spec, err := BuildLaunchSpec(LaunchOptions{
		CursorPath:  cursorPath,
		EndpointURL: "https://127.0.0.1:43123",
		Model:       "relay-gpt",
		CACertPath:  caPath,
		AuthToken:   "local-mock-token",
		ParentEnv: []string{
			"PATH=/usr/bin",
			"KEEP_ME=yes",
			"CURSOR_AUTH_TOKEN=real-cursor-token",
			"CURSOR_API_KEY=real-cursor-api-key",
			"CURSOR_API_ENDPOINT=https://api2.cursor.sh",
			"CURSOR_API_BASE_URL=https://api2.cursor.sh",
			"CURSOR_LOCAL_AGENT_BASE_URL=https://other.example.com",
			"CURSOR_AGENT_CLI_AUTHLESS_MODE=true",
			"NODE_EXTRA_CA_CERTS=/old/ca.pem",
			"RELAY_API_KEY=provider-secret",
		},
		ProviderSecretEnvKeys: []string{"RELAY_API_KEY"},
		UserArgs:              userArgs,
	})
	if err != nil {
		t.Fatalf("BuildLaunchSpec() error = %v", err)
	}
	if spec.Path != cursorPath {
		t.Fatalf("Path = %q, want %q", spec.Path, cursorPath)
	}
	wantArgs := append([]string{
		"-e", "https://127.0.0.1:43123",
		"--model", "relay-gpt",
	}, userArgs...)
	if !reflect.DeepEqual(spec.Args, wantArgs) {
		t.Fatalf("Args = %#v, want %#v", spec.Args, wantArgs)
	}
	wantEnvironment := map[string]string{
		"AGENT_CLI_CREDENTIAL_STORE": "file",
		"PATH":                       "/usr/bin",
		"KEEP_ME":                    "yes",
		"CURSOR_AUTH_TOKEN":          "local-mock-token",
		"CURSOR_API_ENDPOINT":        "https://127.0.0.1:43123",
		"CURSOR_API_BASE_URL":        "https://127.0.0.1:43123",
		"NODE_EXTRA_CA_CERTS":        caPath,
		"NO_OPEN_BROWSER":            "1",
	}
	if got := environmentMap(spec.Env); !reflect.DeepEqual(got, wantEnvironment) {
		t.Fatalf("Env = %#v, want %#v", got, wantEnvironment)
	}
}

func TestBuildLaunchSpecRejectsRemoteEndpointsAndReservedOverrides(t *testing.T) {
	base := LaunchOptions{
		CursorPath:  filepath.Join(t.TempDir(), "cursor-agent"),
		EndpointURL: "https://127.0.0.1:43123",
		Model:       "relay-gpt",
		CACertPath:  filepath.Join(t.TempDir(), "ca.pem"),
		AuthToken:   "local-mock-token",
	}
	tests := []struct {
		name        string
		endpoint    string
		userArgs    []string
		wantInError string
	}{
		{name: "plain HTTP", endpoint: "http://127.0.0.1:43123", wantInError: "HTTPS"},
		{name: "remote host", endpoint: "https://api.cursor.com", wantInError: "loopback"},
		{name: "endpoint override short", userArgs: []string{"-e", "https://api.cursor.com"}, wantInError: "reserved"},
		{name: "endpoint override inline", userArgs: []string{"-e=https://api.cursor.com"}, wantInError: "reserved"},
		{name: "API key override", userArgs: []string{"--api-key", "secret"}, wantInError: "reserved"},
		{name: "model override", userArgs: []string{"--model=other"}, wantInError: "reserved"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			options := base
			if tt.endpoint != "" {
				options.EndpointURL = tt.endpoint
			}
			options.UserArgs = tt.userArgs
			_, err := BuildLaunchSpec(options)
			if err == nil {
				t.Fatal("BuildLaunchSpec() error = nil, want rejection")
			}
			if !strings.Contains(err.Error(), tt.wantInError) {
				t.Fatalf("BuildLaunchSpec() error = %q, want %q", err, tt.wantInError)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("BuildLaunchSpec() error leaked reserved API key value")
			}
		})
	}
}

func TestSplitModelArgumentSelectsOneAliasAndPreservesOtherArguments(t *testing.T) {
	tests := []struct {
		name          string
		args          []string
		wantModel     string
		wantForwarded []string
	}{
		{
			name:          "default",
			args:          []string{"-p", "hello"},
			wantModel:     "default-model",
			wantForwarded: []string{"-p", "hello"},
		},
		{
			name:          "separate value",
			args:          []string{"-p", "--model", "relay-chat", "hello"},
			wantModel:     "relay-chat",
			wantForwarded: []string{"-p", "hello"},
		},
		{
			name:          "inline value",
			args:          []string{"--model=relay-chat", "--", "--model", "literal"},
			wantModel:     "relay-chat",
			wantForwarded: []string{"--", "--model", "literal"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotModel, gotForwarded, err := SplitModelArgument("default-model", tt.args)
			if err != nil {
				t.Fatalf("SplitModelArgument() error = %v", err)
			}
			if gotModel != tt.wantModel || !reflect.DeepEqual(gotForwarded, tt.wantForwarded) {
				t.Fatalf("SplitModelArgument() = %q, %#v; want %q, %#v", gotModel, gotForwarded, tt.wantModel, tt.wantForwarded)
			}
		})
	}
}

func TestSplitModelArgumentRejectsMissingOrDuplicateSelection(t *testing.T) {
	for _, args := range [][]string{
		{"--model"},
		{"--model", "one", "--model=two"},
	} {
		_, _, err := SplitModelArgument("default-model", args)
		if err == nil {
			t.Fatalf("SplitModelArgument(%#v) error = nil, want rejection", args)
		}
		if !strings.Contains(err.Error(), "model") {
			t.Fatalf("SplitModelArgument(%#v) error = %q, want model context", args, err)
		}
	}
}

func TestRunPreservesStandardIOAndChildExitCode(t *testing.T) {
	path := writeExecutable(t, `#!/bin/sh
read value
printf 'out:%s\n' "$value"
printf 'err:%s\n' "$value" >&2
exit 23
`)
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode, err := Run(context.Background(), LaunchSpec{
		Path: path,
		Env:  []string{"FIXTURE=yes"},
	}, strings.NewReader("hello\n"), &stdout, &stderr, nil)

	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if exitCode != 23 {
		t.Fatalf("Run() exit code = %d, want 23", exitCode)
	}
	if stdout.String() != "out:hello\n" || stderr.String() != "err:hello\n" {
		t.Fatalf("stdout = %q, stderr = %q, want inherited child streams", stdout.String(), stderr.String())
	}
}

func TestRunForwardsSignalsToChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal forwarding is required")
	}
	path := writeExecutable(t, `#!/bin/sh
trap 'exit 42' TERM
printf ready > "$1"
while :; do sleep 0.05; done
`)
	readyPath := filepath.Join(t.TempDir(), "ready")
	signals := make(chan os.Signal, 1)
	type result struct {
		exitCode int
		err      error
	}
	resultChannel := make(chan result, 1)
	go func() {
		exitCode, err := Run(context.Background(), LaunchSpec{
			Path: path,
			Args: []string{readyPath},
			Env:  []string{"PATH=/bin:/usr/bin"},
		}, nil, nil, nil, signals)
		resultChannel <- result{exitCode: exitCode, err: err}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not report readiness")
		}
		time.Sleep(10 * time.Millisecond)
	}
	signals <- syscall.SIGTERM

	select {
	case got := <-resultChannel:
		if got.err != nil {
			t.Fatalf("Run() error = %v", got.err)
		}
		if got.exitCode != 42 {
			t.Fatalf("Run() exit code = %d, want trapped signal exit 42", got.exitCode)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not finish after forwarding SIGTERM")
	}
}

func TestRunReportsConventionalExitCodeWhenSignalTerminatesChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix signal exit status is required")
	}
	path := writeExecutable(t, `#!/bin/sh
printf ready > "$1"
while :; do sleep 0.05; done
`)
	readyPath := filepath.Join(t.TempDir(), "ready")
	signals := make(chan os.Signal, 1)
	type result struct {
		exitCode int
		err      error
	}
	resultChannel := make(chan result, 1)
	go func() {
		exitCode, err := Run(context.Background(), LaunchSpec{
			Path: path,
			Args: []string{readyPath},
			Env:  []string{"PATH=/bin:/usr/bin"},
		}, nil, nil, nil, signals)
		resultChannel <- result{exitCode: exitCode, err: err}
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child did not report readiness")
		}
		time.Sleep(10 * time.Millisecond)
	}
	signals <- syscall.SIGTERM

	select {
	case got := <-resultChannel:
		if got.err != nil {
			t.Fatalf("Run() error = %v", got.err)
		}
		if got.exitCode != 128+int(syscall.SIGTERM) {
			t.Fatalf("Run() exit code = %d, want %d", got.exitCode, 128+int(syscall.SIGTERM))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run() did not finish after forwarding SIGTERM")
	}
}

func environmentMap(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if found {
			result[key] = value
		}
	}
	return result
}

func writeExecutable(t *testing.T, contents string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix executable fixtures are required")
	}
	path := filepath.Join(t.TempDir(), "fixture")
	if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}
