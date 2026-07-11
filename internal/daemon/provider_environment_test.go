package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/certs"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	localserver "github.com/shiqkuangsan/cursor-cli-byok/internal/server"
)

func TestProviderEnvironmentOverridesProcessSnapshotWithoutExposingValues(t *testing.T) {
	environment := NewProviderEnvironment(func(name string) string {
		if name == "RELAY_API_KEY" {
			return "old-secret"
		}
		return ""
	})
	if got := environment.Getenv("RELAY_API_KEY"); got != "old-secret" {
		t.Fatalf("initial Getenv() = %q", got)
	}
	environment.Update(map[string]string{"RELAY_API_KEY": "new-secret"})
	if got := environment.Getenv("RELAY_API_KEY"); got != "new-secret" {
		t.Fatalf("updated Getenv() = %q", got)
	}
	for name, text := range map[string]string{
		"String":   environment.String(),
		"GoString": fmt.Sprintf("%#v", environment),
	} {
		if strings.Contains(text, "old-secret") || strings.Contains(text, "new-secret") {
			t.Fatalf("%s leaked a secret: %s", name, text)
		}
	}
}

func TestProviderEnvironmentHandlerAcceptsOnlyConfiguredKeys(t *testing.T) {
	cfg := providerEnvironmentConfig()
	environment := NewProviderEnvironment(func(string) string { return "" })
	handler := NewProviderEnvironmentHandler(func() (config.Config, error) { return cfg, nil }, environment.Update)

	body, _ := json.Marshal(map[string]any{"values": map[string]string{"RELAY_API_KEY": "rotated-secret"}})
	request := httptest.NewRequest(http.MethodPost, ProviderEnvironmentPath, bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || environment.Getenv("RELAY_API_KEY") != "rotated-secret" {
		t.Fatalf("accepted status/value = %d/%q", response.Code, environment.Getenv("RELAY_API_KEY"))
	}
	if strings.Contains(response.Body.String(), "rotated-secret") {
		t.Fatal("control response leaked the provider key")
	}

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "unknown key", body: `{"values":{"OTHER_API_KEY":"secret-value"}}`},
		{name: "empty value", body: `{"values":{"RELAY_API_KEY":""}}`},
		{name: "unknown field", body: `{"values":{"RELAY_API_KEY":"secret-value"},"extra":true}`},
		{name: "trailing JSON", body: `{"values":{"RELAY_API_KEY":"secret-value"}} {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, ProviderEnvironmentPath, strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %q", response.Code, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "secret-value") {
				t.Fatal("rejection response leaked the submitted value")
			}
		})
	}
}

func TestSyncProviderEnvironmentAuthenticatesOverPinnedTLS(t *testing.T) {
	environment := NewProviderEnvironment(func(string) string { return "old-secret" })
	state := startProviderEnvironmentServer(t, providerEnvironmentConfig(), environment)

	err := SyncProviderEnvironment(
		context.Background(),
		state,
		map[string]string{"RELAY_API_KEY": "rotated-secret"},
		time.Second,
	)
	if err != nil {
		t.Fatalf("SyncProviderEnvironment() error = %v", err)
	}
	if got := environment.Getenv("RELAY_API_KEY"); got != "rotated-secret" {
		t.Fatalf("Getenv() = %q, want rotated secret", got)
	}
}

func TestSyncProviderEnvironmentSanitizesAuthenticationAndValidationFailures(t *testing.T) {
	environment := NewProviderEnvironment(func(string) string { return "old-secret" })
	state := startProviderEnvironmentServer(t, providerEnvironmentConfig(), environment)

	wrongAuthState := state
	wrongAuthState.AuthToken = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJjdXJzb3ItY2xpLWJ5b2sifQ.d3Jvbmctc2lnbmF0dXJl"
	for _, test := range []struct {
		name   string
		state  State
		values map[string]string
		secret string
	}{
		{
			name:   "wrong daemon authorization",
			state:  wrongAuthState,
			values: map[string]string{"RELAY_API_KEY": "wrong-auth-secret"},
			secret: "wrong-auth-secret",
		},
		{
			name:   "unknown provider environment key",
			state:  state,
			values: map[string]string{"OTHER_API_KEY": "unknown-key-secret"},
			secret: "unknown-key-secret",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := SyncProviderEnvironment(context.Background(), test.state, test.values, time.Second)
			if err == nil {
				t.Fatal("SyncProviderEnvironment() error = nil")
			}
			if strings.Contains(err.Error(), test.secret) {
				t.Fatalf("SyncProviderEnvironment() error leaked provider secret: %v", err)
			}
		})
	}
}

func providerEnvironmentConfig() config.Config {
	return config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: "relay",
		Models: []config.Model{{
			Name: "relay", Protocol: config.ProtocolOpenAI, BaseURL: "https://provider.example.com",
			Endpoint: config.EndpointResponses, APIKeyEnv: "RELAY_API_KEY", UpstreamModel: "upstream",
		}},
	}
}

func startProviderEnvironmentServer(t *testing.T, cfg config.Config, environment *ProviderEnvironment) State {
	t.Helper()
	handler := NewProviderEnvironmentHandler(func() (config.Config, error) { return cfg, nil }, environment.Update)
	return startDaemonTestServer(t, handler)
}

func startDaemonTestServer(t *testing.T, handler http.Handler) State {
	t.Helper()
	bundle, err := (certs.Manager{Directory: filepath.Join(t.TempDir(), "certs")}).Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	state := validState()
	running, err := localserver.Start(ctx, localserver.Options{
		Certificate:   bundle.Certificate,
		InstanceID:    state.InstanceID,
		AuthToken:     state.AuthToken,
		DaemonVersion: state.DaemonVersion,
		Handler:       handler,
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() {
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = running.Shutdown(shutdownContext)
	})
	endpoint, err := url.Parse(running.EndpointURL())
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	state.PID = os.Getpid()
	state.Port = portFromURL(t, endpoint)
	state.CACertPath = bundle.CACertPath
	return state
}
