package server

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/certs"
)

func TestStartServesHealthOverTrustedHTTP2(t *testing.T) {
	bundle, err := (certs.Manager{Directory: filepath.Join(t.TempDir(), "certs")}).Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	server, err := Start(ctx, Options{
		Certificate:   bundle.Certificate,
		InstanceID:    "0123456789abcdef0123456789abcdef",
		AuthToken:     "test-local-auth-token",
		DaemonVersion: "dev",
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			_, _ = io.WriteString(writer, "application")
		}),
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	defer shutdownServer(t, server)

	client := trustedClient(t, bundle.CACertPath)
	response, err := client.Get(server.EndpointURL() + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}
	if response.ProtoMajor != 2 {
		t.Fatalf("health protocol = %s, want HTTP/2", response.Proto)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", contentType)
	}
	var health HealthResponse
	if err := json.NewDecoder(response.Body).Decode(&health); err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if health.Status != "ok" || health.InstanceID != "0123456789abcdef0123456789abcdef" || health.DaemonVersion != "dev" {
		t.Fatalf("health = %#v, want matching healthy instance", health)
	}

	unauthorizedResponse, err := client.Get(server.EndpointURL() + "/anything")
	if err != nil {
		t.Fatalf("GET unauthorized application handler error = %v", err)
	}
	_ = unauthorizedResponse.Body.Close()
	if unauthorizedResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d, want 401", unauthorizedResponse.StatusCode)
	}
	applicationRequest, err := http.NewRequest(http.MethodGet, server.EndpointURL()+"/anything", nil)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	applicationRequest.Header.Set("Authorization", "Bearer test-local-auth-token")
	applicationResponse, err := client.Do(applicationRequest)
	if err != nil {
		t.Fatalf("GET application handler error = %v", err)
	}
	defer applicationResponse.Body.Close()
	body, err := io.ReadAll(applicationResponse.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(body) != "application" {
		t.Fatalf("application body = %q, want application", body)
	}
}

func TestStartRejectsNonLoopbackAddresses(t *testing.T) {
	bundle, err := (certs.Manager{Directory: filepath.Join(t.TempDir(), "certs")}).Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	for _, address := range []string{"0.0.0.0:0", "[::]:0", "192.0.2.10:43123", ":0", "localhost:0"} {
		t.Run(address, func(t *testing.T) {
			_, err := Start(context.Background(), Options{
				ListenAddress: address,
				Certificate:   bundle.Certificate,
				InstanceID:    "0123456789abcdef0123456789abcdef",
				AuthToken:     "test-local-auth-token",
				DaemonVersion: "dev",
			})
			if err == nil || !strings.Contains(err.Error(), "loopback") {
				t.Fatalf("Start() error = %v, want loopback rejection", err)
			}
		})
	}
}

func TestStartStopsWhenContextIsCanceled(t *testing.T) {
	bundle, err := (certs.Manager{Directory: filepath.Join(t.TempDir(), "certs")}).Ensure()
	if err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	server, err := Start(ctx, Options{
		Certificate:   bundle.Certificate,
		InstanceID:    "0123456789abcdef0123456789abcdef",
		AuthToken:     "test-local-auth-token",
		DaemonVersion: "dev",
	})
	if err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	cancel()

	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Wait() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after context cancellation")
	}
}

func trustedClient(t *testing.T, caPath string) *http.Client {
	t.Helper()
	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		t.Fatalf("ReadFile(CA) error = %v", err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("AppendCertsFromPEM() = false")
	}
	return &http.Client{
		Timeout: 2 * time.Second,
		Transport: &http.Transport{
			ForceAttemptHTTP2: true,
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
				RootCAs:    roots,
			},
		},
	}
}

func shutdownServer(t *testing.T, server *Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := server.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
}
