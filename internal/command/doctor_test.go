package command

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
)

func TestDoctorReportsHealthyHeadlessConfiguration(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodHead || request.Header.Get("Authorization") != "Bearer doctor-secret" {
			t.Errorf("provider request = %s auth=%q", request.Method, request.Header.Get("Authorization"))
		}
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer providerServer.Close()
	app, stdout, stderr := doctorTestApp(t, providerServer.URL, "doctor-secret", "2026.07.08-0c04a8a")

	exitCode := app.Run([]string{"doctor"})

	if exitCode != 0 {
		t.Fatalf("doctor exit = %d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	for _, want := range []string{
		"config: ok (relay-gpt)",
		"cursor-agent: ok (2026.07.08-0c04a8a)",
		"provider: ok (HTTP 405)",
		"daemon: stopped",
		"doctor: ok",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, missing %q", stdout.String(), want)
		}
	}
	if strings.Contains(stdout.String()+stderr.String(), "doctor-secret") || strings.Contains(stdout.String(), providerServer.URL) {
		t.Fatal("doctor leaked API key or provider URL")
	}
}

func TestDoctorWarnsWithoutFailingForUntestedCursorVersion(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer providerServer.Close()
	app, stdout, stderr := doctorTestApp(t, providerServer.URL, "doctor-secret", "2026.07.09-new-build")

	if exitCode := app.Run([]string{"doctor"}); exitCode != 0 {
		t.Fatalf("doctor exit = %d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	if !strings.Contains(stdout.String(), "cursor-agent: warn (untested 2026.07.09-new-build)") || !strings.Contains(stdout.String(), "doctor: ok") {
		t.Fatalf("stdout = %q, want non-fatal untested-version warning", stdout.String())
	}
}

func TestDoctorFallsBackToEmptyPOSTWhenHeadRouteIsMissing(t *testing.T) {
	var methods []string
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.Method)
		if request.Header.Get("Authorization") != "Bearer doctor-secret" {
			t.Errorf("provider authorization is missing")
		}
		switch request.Method {
		case http.MethodHead:
			writer.WriteHeader(http.StatusNotFound)
		case http.MethodPost:
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Errorf("ReadAll() error = %v", err)
			}
			if string(body) != "{}" || request.Header.Get("Content-Type") != "application/json" {
				t.Errorf("provider fallback body/content-type = %q/%q", body, request.Header.Get("Content-Type"))
			}
			writer.WriteHeader(http.StatusBadRequest)
		default:
			t.Errorf("unexpected provider method %s", request.Method)
			writer.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer providerServer.Close()
	app, stdout, stderr := doctorTestApp(t, providerServer.URL, "doctor-secret", "2026.07.08-0c04a8a")

	if exitCode := app.Run([]string{"doctor"}); exitCode != 0 {
		t.Fatalf("doctor exit = %d stderr=%q stdout=%q", exitCode, stderr.String(), stdout.String())
	}
	if strings.Join(methods, ",") != "HEAD,POST" {
		t.Fatalf("provider methods = %v, want HEAD then POST", methods)
	}
	if output := stdout.String() + stderr.String(); !strings.Contains(output, "provider: ok (HTTP 400)") || strings.Contains(output, "doctor-secret") || strings.Contains(output, providerServer.URL) {
		t.Fatalf("doctor output = %q", output)
	}
}

func TestDoctorFailsClosedWithoutLeakingSecrets(t *testing.T) {
	t.Run("provider authentication", func(t *testing.T) {
		providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.WriteHeader(http.StatusUnauthorized)
		}))
		defer providerServer.Close()
		app, stdout, stderr := doctorTestApp(t, providerServer.URL, "top-secret", "2026.07.08-0c04a8a")
		if exitCode := app.Run([]string{"doctor"}); exitCode == 0 {
			t.Fatal("doctor exit = 0")
		}
		output := stdout.String() + stderr.String()
		if !strings.Contains(output, "provider: fail (HTTP 401)") || strings.Contains(output, "top-secret") || strings.Contains(output, providerServer.URL) {
			t.Fatalf("output = %q", output)
		}
	})

	t.Run("missing environment key", func(t *testing.T) {
		app, stdout, stderr := doctorTestApp(t, "https://provider.invalid", "inline-placeholder", "2026.07.08-0c04a8a")
		runtimePaths, _ := paths.Resolve(app.Getenv)
		if err := config.NewStore(runtimePaths.ConfigFile).Save(config.Config{
			Version: config.CurrentVersion, DefaultModel: "relay-gpt",
			Models: []config.Model{{Name: "relay-gpt", Protocol: config.ProtocolOpenAI, BaseURL: "https://provider.invalid", Endpoint: config.EndpointResponses, APIKeyEnv: "MISSING_PROVIDER_KEY", UpstreamModel: "upstream"}},
		}); err != nil {
			t.Fatalf("Save(config) error = %v", err)
		}
		if exitCode := app.Run([]string{"doctor"}); exitCode == 0 {
			t.Fatal("doctor exit = 0")
		}
		output := stdout.String() + stderr.String()
		if !strings.Contains(output, "config: fail (default model API key is unavailable)") || strings.Contains(output, "inline-placeholder") {
			t.Fatalf("output = %q", output)
		}
	})

	t.Run("stale daemon", func(t *testing.T) {
		providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { writer.WriteHeader(http.StatusNoContent) }))
		defer providerServer.Close()
		app, stdout, stderr := doctorTestApp(t, providerServer.URL, "secret", "2026.07.08-0c04a8a")
		saveOperationalState(t, app.Getenv)
		app.ProcessAlive = func(int) bool { return false }
		if exitCode := app.Run([]string{"doctor"}); exitCode == 0 {
			t.Fatal("doctor exit = 0")
		}
		if output := stdout.String() + stderr.String(); !strings.Contains(output, "daemon: fail (stale state)") {
			t.Fatalf("output = %q", output)
		}
	})
}

func doctorTestApp(t *testing.T, providerURL, apiKey, cursorVersion string) (App, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Unix cursor-agent fixture is required")
	}
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatalf("MkdirAll(bin) error = %v", err)
	}
	cursorPath := filepath.Join(binDir, "cursor-agent")
	script := "#!/bin/sh\nprintf '%s\\n' '" + cursorVersion + "'\n"
	if err := os.WriteFile(cursorPath, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile(cursor-agent) error = %v", err)
	}
	values := map[string]string{
		"HOME": home, "PATH": binDir,
		"XDG_CONFIG_HOME": filepath.Join(home, "config"),
		"XDG_DATA_HOME":   filepath.Join(home, "data"),
		"XDG_STATE_HOME":  filepath.Join(home, "state"),
	}
	getenv := commandEnv(values)
	runtimePaths, err := paths.Resolve(getenv)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if err := config.NewStore(runtimePaths.ConfigFile).Save(config.Config{
		Version: config.CurrentVersion, DefaultModel: "relay-gpt",
		Models: []config.Model{{Name: "relay-gpt", Protocol: config.ProtocolOpenAI, BaseURL: providerURL, Endpoint: config.EndpointResponses, APIKey: apiKey, UpstreamModel: "upstream"}},
	}); err != nil {
		t.Fatalf("Save(config) error = %v", err)
	}
	var stdout, stderr bytes.Buffer
	app := App{
		Context: context.Background(), Stdout: &stdout, Stderr: &stderr, Getenv: getenv,
		ProcessAlive: daemon.ProcessAlive, DaemonProbe: daemon.HTTPProbe{},
	}
	return app, &stdout, &stderr
}
